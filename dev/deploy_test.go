package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDokploy records API calls and serves /v1/version, switching to the new
// version only after application.deploy has been called.
type fakeDokploy struct {
	mu       sync.Mutex
	calls    []string
	bodies   []map[string]any
	deployed bool
	status   int // non-zero: every API call fails with this
}

func (f *fakeDokploy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/v1/version" {
		v := "0.1.0"
		if f.deployed {
			v = "0.2.0"
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"version": v})
		return
	}
	if r.Header.Get("x-api-key") != "secret" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if f.status != 0 {
		http.Error(w, "boom", f.status)
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.calls = append(f.calls, strings.TrimPrefix(r.URL.Path, "/api/"))
	f.bodies = append(f.bodies, body)
	if r.URL.Path == "/api/application.deploy" {
		f.deployed = true
	}
}

func testConfig(url string) deployConfig {
	return deployConfig{
		dokployURL: url, apiKey: "secret", applicationID: "app1",
		image: "ghcr.io/tolaniverse/lanternfolk:0.2.0", version: "0.2.0",
		healthURL: url + "/v1/version", timeout: 2 * time.Second, interval: 10 * time.Millisecond,
	}
}

func TestDeploySetsImageDeploysAndWaits(t *testing.T) {
	f := &fakeDokploy{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	if err := deploy(context.Background(), srv.Client(), testConfig(srv.URL)); err != nil {
		t.Fatal(err)
	}
	want := []string{"application.saveDockerProvider", "application.deploy"}
	if strings.Join(f.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", f.calls, want)
	}
	if f.bodies[0]["dockerImage"] != "ghcr.io/tolaniverse/lanternfolk:0.2.0" || f.bodies[0]["applicationId"] != "app1" {
		t.Errorf("saveDockerProvider body = %v", f.bodies[0])
	}
}

func TestDeployFailsOnAPIError(t *testing.T) {
	f := &fakeDokploy{status: http.StatusForbidden}
	srv := httptest.NewServer(f)
	defer srv.Close()
	err := deploy(context.Background(), srv.Client(), testConfig(srv.URL))
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v, want a 403", err)
	}
}

func TestDeployTimesOutIfVersionNeverChanges(t *testing.T) {
	f := &fakeDokploy{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := testConfig(srv.URL)
	c.version = "9.9.9" // never served
	c.timeout = 100 * time.Millisecond
	err := deploy(context.Background(), srv.Client(), c)
	if err == nil || !strings.Contains(err.Error(), "still serving 0.2.0") {
		t.Fatalf("err = %v, want a timeout that names the served version", err)
	}
}

func TestDeployConfigFromEnv(t *testing.T) {
	for _, k := range []string{"DOKPLOY_URL", "DOKPLOY_API_KEY", "DOKPLOY_APPLICATION_ID", "HEALTH_URL", "IMAGE"} {
		t.Setenv(k, "")
	}
	t.Setenv("VERSION", "v1.2.3")
	if _, err := deployConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "DOKPLOY_API_KEY") {
		t.Fatalf("err = %v, want missing-variable error", err)
	}
	t.Setenv("DOKPLOY_URL", "https://d.example/")
	t.Setenv("DOKPLOY_API_KEY", "k")
	t.Setenv("DOKPLOY_APPLICATION_ID", "a")
	t.Setenv("HEALTH_URL", "https://l.example/v1/version")
	c, err := deployConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if c.version != "1.2.3" || c.image != "ghcr.io/tolaniverse/lanternfolk:1.2.3" || c.dokployURL != "https://d.example" {
		t.Errorf("config = %+v", c)
	}
}
