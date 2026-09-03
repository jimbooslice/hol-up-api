package main

import (
	"os"
	"testing"
)

func TestEnv(t *testing.T) {
	const name = "HOLUP_TEST_VALUE"
	t.Cleanup(func() { _ = os.Unsetenv(name) })
	if got := env(name, "fallback"); got != "fallback" {
		t.Fatalf("env() = %q, want fallback", got)
	}
	if err := os.Setenv(name, "configured"); err != nil {
		t.Fatal(err)
	}
	if got := env(name, "fallback"); got != "configured" {
		t.Fatalf("env() = %q, want configured", got)
	}
}
