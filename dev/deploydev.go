package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// deploy-dev: build lanternfolkd with ko, push it to the dev server's
// localhost-only registry through an SSH tunnel, point the Dokploy dev app at
// the new tag, and wait until the service reports it. Needs only Go and SSH.
//
// Non-secret settings live in deploy/dev.env (KEY=value). The Dokploy API key
// comes from DOKPLOY_API_KEY or ~/.config/lanternfolk/dokploy.key.

const koPkg = "github.com/google/ko@v0.19.1"

func deployDevTask([]string) error {
	cfg, err := readEnvFile(filepath.Join("deploy", "dev.env"))
	if err != nil {
		return err
	}
	key := os.Getenv("DOKPLOY_API_KEY")
	if key == "" {
		home, _ := os.UserHomeDir()
		b, err := os.ReadFile(filepath.Join(home, ".config", "lanternfolk", "dokploy.key"))
		if err != nil {
			return errors.New("deploy-dev: set DOKPLOY_API_KEY or write the key to ~/.config/lanternfolk/dokploy.key")
		}
		key = strings.TrimSpace(string(b))
	}
	version, err := devVersion()
	if err != nil {
		return err
	}
	if err := ensureIsolated("ko", koPkg); err != nil {
		return err
	}

	step("tunnel " + cfg["SSH_HOST"] + " → registry")
	local := cfg["LOCAL_REGISTRY_PORT"]
	tunnel := exec.Command("ssh", "-o", "BatchMode=yes", "-o", "ExitOnForwardFailure=yes", "-N",
		"-L", local+":127.0.0.1:"+cfg["REMOTE_REGISTRY_PORT"], cfg["SSH_HOST"])
	tunnel.Stderr = os.Stderr
	if err := tunnel.Start(); err != nil {
		return err
	}
	defer func() { _ = tunnel.Process.Kill(); _ = tunnel.Wait() }()
	if err := waitForPort("127.0.0.1:"+local, 15*time.Second); err != nil {
		return fmt.Errorf("deploy-dev: ssh tunnel: %w", err)
	}

	step("ko build + push " + version)
	ldflags := "-ldflags=-X=github.com/tolaniverse/lanternfolk/internal/buildinfo.Version=" + version
	if err := run([]string{
		"KO_DOCKER_REPO=localhost:" + local + "/" + cfg["IMAGE_REPO"],
		"KO_DEFAULTBASEIMAGE=" + cfg["BASE_IMAGE"],
		"GOFLAGS=" + ldflags,
	}, toolPath("ko"), "build", "./cmd/lanternfolkd", "--bare", "--platform=linux/amd64",
		"--tags=dev,"+version, "--sbom=none"); err != nil {
		return err
	}

	step("dokploy deploy")
	return deploy(context.Background(), http.DefaultClient, deployConfig{
		dokployURL:    cfg["DOKPLOY_URL"],
		apiKey:        key,
		applicationID: cfg["DOKPLOY_APPLICATION_ID"],
		image:         "localhost:" + cfg["REMOTE_REGISTRY_PORT"] + "/" + cfg["IMAGE_REPO"] + ":" + version,
		version:       version,
		healthURL:     cfg["PUBLIC_URL"] + "/v1/version",
		timeout:       3 * time.Minute,
		interval:      3 * time.Second,
	})
}

// devVersion is 0.0.0-dev.<short sha>, with -dirty for uncommitted changes.
func devVersion() (string, error) {
	sha, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w", err)
	}
	v := "0.0.0-dev." + strings.TrimSpace(string(sha))
	if out, _ := exec.Command("git", "status", "--porcelain").Output(); len(strings.TrimSpace(string(out))) > 0 {
		v += "-dirty"
	}
	return v, nil
}

func waitForPort(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			c.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("%s not reachable after %s", addr, timeout)
}

// readEnvFile reads KEY=value lines, ignoring blanks and # comments, and
// fails if any expected key is missing.
func readEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cfg := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s: bad line %q", path, line)
		}
		cfg[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	for _, k := range []string{"SSH_HOST", "LOCAL_REGISTRY_PORT", "REMOTE_REGISTRY_PORT", "IMAGE_REPO",
		"BASE_IMAGE", "DOKPLOY_URL", "DOKPLOY_APPLICATION_ID", "PUBLIC_URL"} {
		if cfg[k] == "" {
			return nil, fmt.Errorf("%s: missing %s", path, k)
		}
	}
	return cfg, sc.Err()
}
