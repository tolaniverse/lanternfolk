// Command lanternfolkd is the Lanternfolk server: one binary for hosted and
// self-hosted deployments (System Design §3).
//
// Configuration comes from flags, each with a LANTERNFOLK_* environment
// variable fallback, so the same binary works under systemd, in a container,
// or on a laptop.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tolaniverse/lanternfolk/internal/adapters/sqlite"
	"github.com/tolaniverse/lanternfolk/internal/buildinfo"
	"github.com/tolaniverse/lanternfolk/internal/server/app"
	"github.com/tolaniverse/lanternfolk/internal/server/gateway"
	"github.com/tolaniverse/lanternfolk/internal/server/realtime"
)

type config struct {
	addr            string
	logLevel        slog.Level
	logFormat       string
	shutdownTimeout time.Duration
	database        string
	devAuth         bool
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "lanternfolkd:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stderr *os.File) error {
	cfg, showVersion, err := parse(args)
	if err != nil {
		return err
	}
	if showVersion {
		fmt.Println(buildinfo.Get())
		return nil
	}

	log := newLogger(cfg, stderr)
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := sqlite.Open(ctx, cfg.database)
	if err != nil {
		return err
	}
	defer store.Close()
	wake := make(chan struct{}, 1)
	hub := realtime.New()
	a := app.New(store, func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	})
	relayCtx, stopRelay := context.WithCancel(ctx)
	defer stopRelay()
	go func() {
		if err := realtime.Relay(relayCtx, store, hub.Publish, wake); err != nil && relayCtx.Err() == nil {
			log.Error("outbox relay stopped", "error", err)
		}
	}()
	gw := gateway.New(log).WithWorkspaceLoop(a, hub, cfg.devAuth)
	srv := &http.Server{
		Addr:              cfg.addr,
		Handler:           gw,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	ln, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return err
	}
	log.Info("listening", "addr", ln.Addr().String(), "version", buildinfo.Get().String())
	gw.SetReady(true)

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}

	// Drain: stop advertising readiness, then let in-flight requests finish.
	log.Info("shutting down", "timeout", cfg.shutdownTimeout)
	gw.SetReady(false)
	sctx, cancel := context.WithTimeout(context.Background(), cfg.shutdownTimeout)
	defer cancel()
	return srv.Shutdown(sctx)
}

func parse(args []string) (config, bool, error) {
	fs := flag.NewFlagSet("lanternfolkd", flag.ContinueOnError)
	cfg := config{}
	var level string
	fs.StringVar(&cfg.addr, "addr", env("LANTERNFOLK_ADDR", ":8080"), "listen address")
	fs.StringVar(&level, "log-level", env("LANTERNFOLK_LOG_LEVEL", "info"), "debug, info, warn, or error")
	fs.StringVar(&cfg.logFormat, "log-format", env("LANTERNFOLK_LOG_FORMAT", "json"), "json or text")
	fs.DurationVar(&cfg.shutdownTimeout, "shutdown-timeout", 20*time.Second, "how long to drain on SIGTERM")
	fs.StringVar(&cfg.database, "database", env("LANTERNFOLK_DATABASE", "lanternfolk.db"), "SQLite database path")
	fs.BoolVar(&cfg.devAuth, "dev-auth", false, "enable handle-only dev sign-in (local development only)")
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return cfg, false, err
	}
	if err := cfg.logLevel.UnmarshalText([]byte(level)); err != nil {
		return cfg, false, fmt.Errorf("-log-level: %w", err)
	}
	if cfg.logFormat != "json" && cfg.logFormat != "text" {
		return cfg, false, fmt.Errorf("-log-format: want json or text, got %q", cfg.logFormat)
	}
	return cfg, *showVersion, nil
}

func newLogger(cfg config, w *os.File) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.logLevel}
	if cfg.logFormat == "text" {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}
