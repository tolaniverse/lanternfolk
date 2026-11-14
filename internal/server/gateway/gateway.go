// Package gateway is lanternfolkd's HTTP edge. For now it serves health and
// version endpoints. The WebSocket endpoint (/v1/ws) and the REST API from
// System Design §4 land here in slice 1.
package gateway

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/tolaniverse/lanternfolk/internal/buildinfo"
	"github.com/tolaniverse/lanternfolk/protocol"
)

// Gateway holds the HTTP handlers and readiness state.
type Gateway struct {
	log   *slog.Logger
	ready atomic.Bool
	mux   *http.ServeMux
}

// New builds a gateway. It reports not-ready until SetReady(true).
func New(log *slog.Logger) *Gateway {
	g := &Gateway{log: log, mux: http.NewServeMux()}
	g.mux.HandleFunc("GET /healthz", g.healthz)
	g.mux.HandleFunc("GET /readyz", g.readyz)
	g.mux.HandleFunc("GET /v1/version", g.version)
	return g
}

// SetReady flips readiness, e.g. after the database is reachable, or to
// drain traffic before shutdown.
func (g *Gateway) SetReady(ok bool) { g.ready.Store(ok) }

// ServeHTTP logs each request and dispatches it.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	g.mux.ServeHTTP(rec, r)
	g.log.LogAttrs(r.Context(), slog.LevelDebug, "http",
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.Int("status", rec.status),
		slog.Duration("took", time.Since(start)),
	)
}

// healthz: the process is alive.
func (g *Gateway) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

// readyz: the process should receive traffic.
func (g *Gateway) readyz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if !g.ready.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready\n"))
		return
	}
	_, _ = w.Write([]byte("ready\n"))
}

type versionResponse struct {
	buildinfo.Info
	Protocol int `json:"protocol"`
}

func (g *Gateway) version(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(versionResponse{Info: buildinfo.Get(), Protocol: protocol.Version})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
