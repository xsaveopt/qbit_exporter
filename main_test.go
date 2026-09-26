package main

import (
	"flag"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func healthServer(t *testing.T, status int) (addr string, hits *atomic.Int64, paths chan string) {
	t.Helper()
	hits = &atomic.Int64{}
	paths = make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		select {
		case paths <- r.URL.Path:
		default:
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener address: %v", err)
	}
	return ":" + port, hits, paths
}

func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	_ = ln.Close()
	return port
}

func TestProbeHealth(t *testing.T) {
	t.Run("healthy instance", func(t *testing.T) {
		addr, hits, paths := healthServer(t, http.StatusOK)
		if err := probeHealth(addr); err != nil {
			t.Fatalf("probeHealth: %v", err)
		}
		if n := hits.Load(); n != 1 {
			t.Errorf("requests = %d, want 1", n)
		}
		if p := <-paths; p != "/health" {
			t.Errorf("path = %q, want /health", p)
		}
	})

	t.Run("degraded instance", func(t *testing.T) {
		addr, _, _ := healthServer(t, http.StatusServiceUnavailable)
		err := probeHealth(addr)
		if err == nil {
			t.Fatal("probeHealth succeeded against a 503")
		}
		if !strings.Contains(err.Error(), "status 503") {
			t.Errorf("error = %q, want it to mention status 503", err)
		}
	})

	t.Run("redirect to an unhealthy target fails", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/health" {
				http.Redirect(w, r, "/elsewhere", http.StatusFound)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
		if err := probeHealth(":" + port); err == nil {
			t.Fatal("probeHealth succeeded after a redirect to a 500")
		}
	})

	t.Run("host in the listen address is ignored", func(t *testing.T) {
		addr, _, _ := healthServer(t, http.StatusOK)
		port := strings.TrimPrefix(addr, ":")
		if err := probeHealth("0.0.0.0:" + port); err != nil {
			t.Fatalf("probeHealth: %v", err)
		}
		if err := probeHealth("[::]:" + port); err != nil {
			t.Fatalf("probeHealth with an IPv6 wildcard: %v", err)
		}
	})

	t.Run("nothing listening", func(t *testing.T) {
		if err := probeHealth(":" + closedPort(t)); err == nil {
			t.Fatal("probeHealth succeeded with nothing listening")
		}
	})

	t.Run("unparseable address falls back to the default port", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:9879")
		if err != nil {
			t.Skipf("default port unavailable: %v", err)
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/health" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
		}), ReadHeaderTimeout: time.Second}
		go func() { _ = srv.Serve(ln) }()
		defer func() { _ = srv.Close() }()

		for _, addr := range []string{"not-an-address", "", "127.0.0.1:"} {
			if err := probeHealth(addr); err != nil {
				t.Errorf("probeHealth(%q): %v, want the default port to be probed", addr, err)
			}
		}
	})
}

func runWithArgs(t *testing.T, args ...string) error {
	t.Helper()
	oldArgs, oldFlags := os.Args, flag.CommandLine
	t.Cleanup(func() {
		os.Args = oldArgs
		flag.CommandLine = oldFlags
	})
	flag.CommandLine = flag.NewFlagSet("qbit_exporter", flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)
	os.Args = append([]string{"qbit_exporter"}, args...)
	return run()
}

func TestRunVersion(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	oldStdout := os.Stdout
	os.Stdout = w
	runErr := runWithArgs(t, "-version")
	os.Stdout = oldStdout
	_ = w.Close()
	out, _ := io.ReadAll(r)
	_ = r.Close()

	if runErr != nil {
		t.Fatalf("run -version: %v", runErr)
	}
	if got := string(out); got != "qbit_exporter "+version+"\n" {
		t.Errorf("output = %q", got)
	}
}

func TestRunHealthcheck(t *testing.T) {
	t.Run("flag address", func(t *testing.T) {
		addr, hits, _ := healthServer(t, http.StatusOK)
		if err := runWithArgs(t, "-healthcheck", "-web.listen-address="+addr); err != nil {
			t.Fatalf("run -healthcheck: %v", err)
		}
		if n := hits.Load(); n != 1 {
			t.Errorf("requests = %d, want 1", n)
		}
	})

	t.Run("environment address", func(t *testing.T) {
		addr, _, _ := healthServer(t, http.StatusServiceUnavailable)
		t.Setenv("QBIT_EXPORTER_ADDR", addr)
		err := runWithArgs(t, "-healthcheck")
		if err == nil || !strings.Contains(err.Error(), "status 503") {
			t.Errorf("run -healthcheck = %v, want a 503 status error", err)
		}
	})
}

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
