package main

import (
	"testing"

	"github.com/spf13/pflag"

	"github.com/rebuno/rebuno/internal/config"
)

func TestServerFlagOverridesEnv(t *testing.T) {
	t.Setenv("REBUNO_LISTEN_ADDR", ":7000")

	cfg := config.FromEnv()
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	bindServerFlags(fs, &cfg)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":7000" {
		t.Fatalf("expected env value :7000, got %q", cfg.ListenAddr)
	}

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
