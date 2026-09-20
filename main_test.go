package main

import (
	"testing"
	"time"
)

func TestEnv(t *testing.T) {
	const key = "QBIT_EXPORTER_TEST_STRING"

	t.Run("unset falls back", func(t *testing.T) {
		if got := env(key, "fallback"); got != "fallback" {
			t.Errorf("env = %q, want %q", got, "fallback")
		}
	})

	t.Run("set wins", func(t *testing.T) {
		t.Setenv(key, "http://qbit.example.org:8080")
		if got := env(key, "fallback"); got != "http://qbit.example.org:8080" {
			t.Errorf("env = %q", got)
		}
	})

	t.Run("empty set value wins over the default", func(t *testing.T) {
		t.Setenv(key, "")
		if got := env(key, "fallback"); got != "" {
			t.Errorf("env = %q, want the empty value that was actually set", got)
		}
	})
}

func TestEnvBool(t *testing.T) {
	const key = "QBIT_EXPORTER_TEST_BOOL"

	t.Run("unset falls back", func(t *testing.T) {
		if !envBool(key, true) {
			t.Error("envBool = false, want the default")
		}
		if envBool(key, false) {
			t.Error("envBool = true, want the default")
		}
	})

	tests := []struct {
		value string
		def   bool
		want  bool
	}{
		{value: "true", def: false, want: true},
		{value: "TRUE", def: false, want: true},
		{value: "True", def: false, want: true},
		{value: "1", def: false, want: true},
		{value: "t", def: false, want: true},
		{value: "false", def: true, want: false},
		{value: "0", def: true, want: false},
		{value: "f", def: true, want: false},
		{value: "FALSE", def: true, want: false},
		{value: "yes", def: true, want: true},
		{value: "no", def: false, want: false},
		{value: "", def: true, want: true},
		{value: "maybe", def: true, want: true},
		{value: "2", def: false, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			t.Setenv(key, tt.value)
			if got := envBool(key, tt.def); got != tt.want {
				t.Errorf("envBool(%q, %v) = %v, want %v", tt.value, tt.def, got, tt.want)
			}
		})
	}
}

func TestEnvDuration(t *testing.T) {
	const key = "QBIT_EXPORTER_TEST_DURATION"

	t.Run("unset falls back", func(t *testing.T) {
		if got := envDuration(key, 10*time.Second); got != 10*time.Second {
			t.Errorf("envDuration = %v, want 10s", got)
		}
	})

	tests := []struct {
		value string
		want  time.Duration
	}{
		{value: "30s", want: 30 * time.Second},
		{value: "1h", want: time.Hour},
		{value: "1h30m", want: 90 * time.Minute},
		{value: "250ms", want: 250 * time.Millisecond},
		{value: "0", want: 0},
		{value: "-5s", want: -5 * time.Second},
		{value: "10", want: time.Minute},
		{value: "", want: time.Minute},
		{value: "forever", want: time.Minute},
		{value: "1 hour", want: time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			t.Setenv(key, tt.value)
			if got := envDuration(key, time.Minute); got != tt.want {
				t.Errorf("envDuration(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}
