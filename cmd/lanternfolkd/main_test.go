package main

import (
	"log/slog"
	"testing"
)

func TestParseDefaults(t *testing.T) {
	t.Setenv("LANTERNFOLK_ADDR", "127.0.0.1:9999")
	cfg, version, err := parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if version || cfg.addr != "127.0.0.1:9999" || cfg.logLevel != slog.LevelInfo || cfg.logFormat != "json" {
		t.Errorf("cfg = %+v, version = %v", cfg, version)
	}
}

func TestParseFlagsOverrideEnv(t *testing.T) {
	t.Setenv("LANTERNFOLK_ADDR", ":1")
	cfg, _, err := parse([]string{"-addr", ":2", "-log-level", "debug", "-log-format", "text"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.addr != ":2" || cfg.logLevel != slog.LevelDebug || cfg.logFormat != "text" {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestParseRejectsBadValues(t *testing.T) {
	for _, args := range [][]string{
		{"-log-level", "loud"},
		{"-log-format", "xml"},
	} {
		if _, _, err := parse(args); err == nil {
			t.Errorf("parse(%v) succeeded, want error", args)
		}
	}
}
