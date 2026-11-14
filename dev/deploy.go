package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// deployConfig is read from the environment, so CI passes secrets without
// them appearing in arguments or logs.
type deployConfig struct {
	dokployURL    string // https://dokploy.example.com
	apiKey        string // Dokploy: Settings › Profile › API/CLI
	applicationID string // the Dokploy application to update
	image         string // ghcr.io/tolaniverse/lanternfolk:0.1.0
	version       string // what /v1/version must report once rolled out
	healthURL     string // https://lanternfolk.example.com/v1/version
	timeout       time.Duration
	interval      time.Duration
}

func deployConfigFromEnv() (deployConfig, error) {
	version := strings.TrimPrefix(os.Getenv("VERSION"), "v")
	c := deployConfig{
		dokployURL:    strings.TrimRight(os.Getenv("DOKPLOY_URL"), "/"),
		apiKey:        os.Getenv("DOKPLOY_API_KEY"),
		applicationID: os.Getenv("DOKPLOY_APPLICATION_ID"),
		image:         os.Getenv("IMAGE"),
		version:       version,
		healthURL:     os.Getenv("HEALTH_URL"),
		timeout:       5 * time.Minute,
		interval:      5 * time.Second,
	}
	if c.image == "" && version != "" {
		c.image = "ghcr.io/tolaniverse/lanternfolk:" + version
	}
	var missing []string
	for name, v := range map[string]string{
		"DOKPLOY_URL": c.dokployURL, "DOKPLOY_API_KEY": c.apiKey,
		"DOKPLOY_APPLICATION_ID": c.applicationID, "VERSION": c.version, "HEALTH_URL": c.healthURL,
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return c, fmt.Errorf("deploy: set %s", strings.Join(missing, ", "))
	}
	return c, nil
}

// deployTask points the Dokploy application at the release's image, deploys
// it, and waits until the service reports the new version.
func deployTask([]string) error {
	c, err := deployConfigFromEnv()
	if err != nil {
		return err
	}
	return deploy(context.Background(), http.DefaultClient, c)
}

func deploy(ctx context.Context, hc *http.Client, c deployConfig) error {
	fmt.Printf("  image → %s\n", c.image)
	if err := dokploy(ctx, hc, c, "application.saveDockerProvider", map[string]any{
		"applicationId": c.applicationID,
		"dockerImage":   c.image,
		"username":      nil, // public GHCR image; set credentials in Dokploy for a private one
		"password":      nil,
		"registryUrl":   nil,
	}); err != nil {
		return err
	}
	fmt.Println("  deploying")
	if err := dokploy(ctx, hc, c, "application.deploy", map[string]any{
		"applicationId": c.applicationID,
		"title":         "release " + c.version,
	}); err != nil {
		return err
	}
	fmt.Printf("  waiting for %s to report %s\n", c.healthURL, c.version)
	return waitForVersion(ctx, hc, c)
}

func dokploy(ctx context.Context, hc *http.Client, c deployConfig, endpoint string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.dokployURL+"/api/"+endpoint, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	res, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("dokploy %s: %w", endpoint, err)
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("dokploy %s: %s: %s", endpoint, res.Status, bytes.TrimSpace(msg))
	}
	return nil
}

func waitForVersion(ctx context.Context, hc *http.Client, c deployConfig) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	last := "no response"
	for {
		if v, err := currentVersion(ctx, hc, c.healthURL); err != nil {
			last = err.Error()
		} else if v == c.version {
			fmt.Printf("\n✓ %s is serving %s\n", c.healthURL, v)
			return nil
		} else {
			last = "still serving " + v
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("deploy: %s did not report %s within %s (last: %s)", c.healthURL, c.version, c.timeout, last)
		case <-time.After(c.interval):
		}
	}
}

func currentVersion(ctx context.Context, hc *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	res, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", errors.New(res.Status)
	}
	var body struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return "", err
	}
	return body.Version, nil
}
