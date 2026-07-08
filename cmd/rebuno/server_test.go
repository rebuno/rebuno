package main

import (
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/rebuno/rebuno/internal/config"
)

func TestServerFlagOverridesEnv(t *testing.T) {
	t.Setenv("REBUNO_LISTEN_ADDR", ":7000")
	cfg := config.FromEnv() // env wins over default -> :7000
	if cfg.ListenAddr != ":7000" {
		t.Fatalf("env not applied: %q", cfg.ListenAddr)
	}
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	bindServerFlags(fs, &cfg)

	// No flag passed: env value stays.
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":7000" {
		t.Fatalf("expected env value :7000, got %q", cfg.ListenAddr)
	}

	// Explicit flag overrides env.
	cfg2 := config.FromEnv()
	fs2 := pflag.NewFlagSet("test2", pflag.ContinueOnError)
	bindServerFlags(fs2, &cfg2)
	if err := fs2.Parse([]string{"--listen-addr=:9999"}); err != nil {
		t.Fatal(err)
	}
	if cfg2.ListenAddr != ":9999" {
		t.Fatalf("flag should override env, got %q", cfg2.ListenAddr)
	}
}

func TestServerCmdRejectsMissingToken(t *testing.T) {
	t.Setenv("REBUNO_DB_URL", "")
	t.Setenv("REBUNO_BEARER_TOKEN", "")
	cmd := serverCmd()
	cmd.SetArgs([]string{"--db-url=postgres://localhost/x"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "BEARER_TOKEN") {
		t.Fatalf("expected bearer-token validation error, got %v", err)
	}
}

func TestServerCmdRejectsMissingDBURL(t *testing.T) {
	t.Setenv("REBUNO_DB_URL", "")
	t.Setenv("REBUNO_BEARER_TOKEN", "")
	cmd := serverCmd()
	cmd.SetArgs(nil)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "REBUNO_DB_URL") {
		t.Fatalf("expected db-url validation error, got %v", err)
	}
}
