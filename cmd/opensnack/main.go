// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"opensnack/internal/auth"
	"opensnack/internal/db"
	"opensnack/internal/limits"
	"opensnack/internal/logging"
	"opensnack/internal/resource"
	"opensnack/internal/router"
	"opensnack/internal/telemetry"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"go.uber.org/zap"
)

func main() {
	logger, err := logging.New()
	if err != nil {
		panic(err)
	}
	zap.ReplaceGlobals(logger)
	defer logger.Sync()

	shutdownTelemetry, err := telemetry.Setup(context.Background(), "")
	if err != nil {
		zap.L().Fatal("telemetry", zap.Error(err))
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTelemetry(ctx)
	}()

	pg := db.Connect() // returns *gorm.DB
	if err := db.Migrate(pg); err != nil {
		zap.L().Fatal("database migration failed", zap.Error(err))
	}
	store := resource.NewGormStore(pg)

	authn, err := auth.FromEnv()
	if err != nil {
		zap.L().Fatal("auth configuration", zap.Error(err))
	}
	zap.L().Info("auth configured", zap.String("mode", string(authn.Mode)))

	app := router.NewServer(store, router.Config{
		Auth:          authn,
		InternalToken: os.Getenv("OPENSNACK_INTERNAL_TOKEN"),
		Limits:        limits.FromEnv(),
	})
	if app.Scheduler != nil {
		app.Scheduler.Start()
	}
	if app.ESM != nil {
		app.ESM.Start()
	}
	if err := app.Lambda.Start(); err != nil {
		zap.L().Fatal("lambda executor", zap.Error(err))
	}

	addr := os.Getenv("OPENSNACK_LISTEN_ADDR")
	if addr == "" {
		addr = ":4566"
	}

	var handler http.Handler = app
	if telemetry.Enabled() {
		// A server span per request (named by router.TelemetryMiddleware),
		// continuing the caller's trace; not for health checks.
		handler = otelhttp.NewHandler(app, "opensnack", otelhttp.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != "/_opensnack/health"
		}))
	}
	srv := &http.Server{
		Addr:           addr,
		Handler:        handler,
		ReadTimeout:    15 * time.Second,
		WriteTimeout:   15 * time.Second,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()

	zap.L().Info("listening", zap.String("addr", addr))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		zap.L().Fatal("http server exited",
			zap.Error(err),
		)
	}
	if app.Scheduler != nil {
		app.Scheduler.Stop()
	}
	if app.ESM != nil {
		app.ESM.Stop()
	}
	app.Lambda.Stop()
}
