package http

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// readinessTimeout bounds how long the readiness probe waits on dependencies.
const readinessTimeout = 2 * time.Second

// pinger abstracts a backing dependency's health check.
type pinger interface {
	Ping(ctx context.Context) error
}

// HealthHandler serves liveness and readiness.
type HealthHandler struct {
	db    pinger
	redis pinger
}

// NewHealthHandler constructs the handler.
func NewHealthHandler(db, redis pinger) *HealthHandler {
	return &HealthHandler{db: db, redis: redis}
}

// Live reports process liveness (always 200 if serving).
func (h *HealthHandler) Live(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Ready checks Postgres and Redis.
func (h *HealthHandler) Ready(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), readinessTimeout)
	defer cancel()

	checks := gin.H{"postgres": "ok", "redis": "ok"}
	ready := true
	if err := h.db.Ping(ctx); err != nil {
		checks["postgres"] = "unavailable"
		ready = false
	}
	if err := h.redis.Ping(ctx); err != nil {
		checks["redis"] = "unavailable"
		ready = false
	}
	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}
	c.JSON(status, gin.H{"ready": ready, "checks": checks})
}
