package gateway

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tolaniverse/lanternfolk/protocol"
)

func newTest(t *testing.T) (*Gateway, *httptest.Server) {
	t.Helper()
	g := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	return g, srv
}

func get(t *testing.T, url string) *http.Response {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func TestHealthz(t *testing.T) {
	_, srv := newTest(t)
	if res := get(t, srv.URL+"/healthz"); res.StatusCode != http.StatusOK {
		t.Errorf("status = %d", res.StatusCode)
	}
}

func TestReadyzFollowsState(t *testing.T) {
	g, srv := newTest(t)
	if res := get(t, srv.URL+"/readyz"); res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("before ready: status = %d, want 503", res.StatusCode)
	}
	g.SetReady(true)
	if res := get(t, srv.URL+"/readyz"); res.StatusCode != http.StatusOK {
		t.Errorf("after ready: status = %d, want 200", res.StatusCode)
	}
}

func TestVersion(t *testing.T) {
	_, srv := newTest(t)
	res := get(t, srv.URL+"/v1/version")
	var body struct {
		Version  string `json:"version"`
		Protocol int    `json:"protocol"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Version == "" || body.Protocol != protocol.Version {
		t.Errorf("body = %+v", body)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	_, srv := newTest(t)
	res, err := http.Post(srv.URL+"/healthz", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", res.StatusCode)
	}
}
