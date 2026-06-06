package platform

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/assafbh/identityhub/internal/config"
)

const (
	pprofReadHeaderTimeout = 5 * time.Second
	defaultShutdownTimeout = 15 * time.Second
)

// Server wraps the public HTTP server and the optional internal pprof server,
// coordinating startup and graceful shutdown.
type Server struct {
	log    *slog.Logger
	public *http.Server
	pprof  *http.Server
	cfg    config.HTTPConfig
}

// NewServer constructs the servers. handler serves public traffic; pprof (if
// enabled) is bound to a separate, internal-only address.
func NewServer(log *slog.Logger, httpCfg config.HTTPConfig, pprofCfg config.PprofConfig, handler http.Handler) *Server {
	s := &Server{
		log: log,
		cfg: httpCfg,
		public: &http.Server{
			Addr:         httpCfg.Addr,
			Handler:      handler,
			ReadTimeout:  httpCfg.ReadTimeout,
			WriteTimeout: httpCfg.WriteTimeout,
			IdleTimeout:  httpCfg.IdleTimeout,
		},
	}
	if pprofCfg.Enabled {
		s.pprof = &http.Server{
			Addr:              pprofCfg.Addr,
			Handler:           pprofMux(),
			ReadHeaderTimeout: pprofReadHeaderTimeout,
		}
	}
	return s
}

// pprofMux builds a mux exposing the standard pprof handlers. It is served only
// on the internal pprof address and never mounted on the public router.
func pprofMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

// Run starts the servers and blocks until ctx is cancelled, then drains
// in-flight requests within the configured shutdown timeout.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 2)

	go func() {
		s.log.Info("public http server listening", slog.String("addr", s.public.Addr))
		if err := s.public.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	if s.pprof != nil {
		go func() {
			s.log.Info("internal pprof server listening", slog.String("addr", s.pprof.Addr))
			if err := s.pprof.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()
	}

	select {
	case <-ctx.Done():
		return s.shutdown()
	case err := <-errCh:
		return err
	}
}

func (s *Server) shutdown() error {
	timeout := s.cfg.ShutdownTimeout
	if timeout <= 0 {
		timeout = defaultShutdownTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	s.log.Info("shutting down servers", slog.Duration("timeout", timeout))
	if s.pprof != nil {
		_ = s.pprof.Shutdown(ctx)
	}
	return s.public.Shutdown(ctx)
}
