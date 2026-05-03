// Package config loads runtime configuration from environment variables.
// 12-factor by design: no config files, no flags, just env vars. Values
// have sensible defaults so docker-compose just works without a .env.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// Core is the configuration for the cmd/core binary.
type Core struct {
	HTTPAddr        string        // address the HTTP server binds to (e.g. ":8080")
	PostgresDSN     string        // postgres://user:pass@host:port/db?sslmode=disable
	AMQPURL         string        // amqp://user:pass@host:port/
	DefaultTenantID uuid.UUID     // single-tenant fallback
	StartupAttempts int           // dependency-wait retry count
	StartupDelay    time.Duration // delay between dep-wait attempts
}

// LoadCore reads env vars and returns a Core config or an error if a
// required value is missing or unparseable.
func LoadCore() (Core, error) {
	cfg := Core{
		HTTPAddr:        env("HTTP_ADDR", ":8080"),
		PostgresDSN:     env("POSTGRES_DSN", "postgres://soar:soar@localhost:5432/soar?sslmode=disable"),
		AMQPURL:         env("AMQP_URL", "amqp://soar:soar@localhost:5672/"),
		StartupAttempts: envInt("STARTUP_ATTEMPTS", 30),
		StartupDelay:    time.Duration(envInt("STARTUP_DELAY_MS", 1000)) * time.Millisecond,
	}
	tenantStr := env("DEFAULT_TENANT_ID", "00000000-0000-0000-0000-000000000001")
	tenant, err := uuid.Parse(tenantStr)
	if err != nil {
		return Core{}, fmt.Errorf("invalid DEFAULT_TENANT_ID: %w", err)
	}
	cfg.DefaultTenantID = tenant
	return cfg, nil
}

// Connector is the configuration for the reference-connector binary.
type Connector struct {
	ConnectorID     string
	CoreAPIBase     string        // e.g. http://core:8080
	AMQPURL         string
	TenantID        uuid.UUID
	HeartbeatPeriod time.Duration
	StartupAttempts int
	StartupDelay    time.Duration
}

// LoadConnector reads env vars for the reference connector.
func LoadConnector() (Connector, error) {
	cfg := Connector{
		ConnectorID:     env("CONNECTOR_ID", "reference-connector"),
		CoreAPIBase:     env("CORE_API_BASE", "http://localhost:8080"),
		AMQPURL:         env("AMQP_URL", "amqp://soar:soar@localhost:5672/"),
		HeartbeatPeriod: time.Duration(envInt("HEARTBEAT_MS", 30_000)) * time.Millisecond,
		StartupAttempts: envInt("STARTUP_ATTEMPTS", 30),
		StartupDelay:    time.Duration(envInt("STARTUP_DELAY_MS", 1000)) * time.Millisecond,
	}
	tenantStr := env("DEFAULT_TENANT_ID", "00000000-0000-0000-0000-000000000001")
	tenant, err := uuid.Parse(tenantStr)
	if err != nil {
		return Connector{}, fmt.Errorf("invalid DEFAULT_TENANT_ID: %w", err)
	}
	cfg.TenantID = tenant
	return cfg, nil
}

// env returns the named env var or the fallback.
func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// envInt parses an env var as int, defaulting to fallback on absent/parse-fail.
func envInt(name string, fallback int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
