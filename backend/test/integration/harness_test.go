//go:build integration

// Package integration runs end-to-end tests against real Postgres + Redis
// (testcontainers) with a mocked Jira. Run with: `task test-integration`.
package integration

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/assafbh/identityhub/internal/config"
	"github.com/assafbh/identityhub/internal/platform"
)

const (
	superUser    = "ih_admin"
	superPass    = "admin_pw"
	appPass      = "app_pw"
	dbName       = "identityhub"
	cryptoKeyB64 = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=" // 32 bytes
)

// shared across the suite, set up in TestMain.
var (
	adminPool   *pgxpool.Pool // superuser — for cross-tenant/RLS assertions
	appPool     *pgxpool.Pool // least-privilege app role — what the app uses
	redisClient *goredis.Client
	appDSN      string
	pgHost      string
	pgPort      int
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	pgC, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase(dbName),
		tcpostgres.WithUsername(superUser),
		tcpostgres.WithPassword(superPass),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		log.Fatalf("start postgres: %v", err)
	}
	defer func() { _ = pgC.Terminate(ctx) }()

	redisC, err := tcredis.Run(ctx, "redis:7-alpine")
	if err != nil {
		log.Fatalf("start redis: %v", err)
	}
	defer func() { _ = redisC.Terminate(ctx) }()

	superDSN, err := pgC.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		log.Fatalf("pg conn string: %v", err)
	}

	// Run migrations as the superuser.
	mig, err := migrate.New("file://../../migrations", superDSN)
	if err != nil {
		log.Fatalf("migrate new: %v", err)
	}
	if err := mig.Up(); err != nil && err != migrate.ErrNoChange {
		log.Fatalf("migrate up: %v", err)
	}

	adminPool, err = pgxpool.New(ctx, superDSN)
	if err != nil {
		log.Fatalf("admin pool: %v", err)
	}
	defer adminPool.Close()

	// The migration created identityhub_app NOLOGIN; enable login for the app pool.
	if _, err := adminPool.Exec(ctx, fmt.Sprintf("ALTER ROLE identityhub_app LOGIN PASSWORD '%s'", appPass)); err != nil {
		log.Fatalf("enable app role: %v", err)
	}

	host, _ := pgC.Host(ctx)
	port, _ := pgC.MappedPort(ctx, "5432/tcp")
	// Build the app pool through the production platform constructor so it is
	// exercised by the integration suite (timeouts/GUCs and all).
	portNum, _ := strconv.Atoi(port.Port())
	pgHost, pgPort = host, portNum
	appDSN = fmt.Sprintf("postgres://identityhub_app:%s@%s:%s/%s?sslmode=disable", appPass, host, port.Port(), dbName)
	appPool, err = platform.NewPostgresPool(ctx, config.PostgresConfig{
		Host: host, Port: portNum, User: "identityhub_app", Password: appPass, DB: dbName,
		SSLMode: "disable", MaxConns: 10, StatementTimeout: 30 * time.Second, IdleInTxTimeout: 15 * time.Second,
	})
	if err != nil {
		log.Fatalf("app pool: %v", err)
	}
	defer appPool.Close()

	redisAddr, err := redisC.ConnectionString(ctx)
	if err != nil {
		log.Fatalf("redis conn string: %v", err)
	}
	opt, err := goredis.ParseURL(redisAddr)
	if err != nil {
		log.Fatalf("parse redis url: %v", err)
	}
	redisClient, err = platform.NewRedisClient(ctx, config.RedisConfig{Addr: opt.Addr, Password: opt.Password, DB: opt.DB})
	if err != nil {
		log.Fatalf("redis client: %v", err)
	}
	defer func() { _ = redisClient.Close() }()

	os.Exit(m.Run())
}
