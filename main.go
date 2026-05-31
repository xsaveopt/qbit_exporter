package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var version = "dev"

func main() {
	var (
		listenAddr  = flag.String("web.listen-address", env("QBIT_EXPORTER_ADDR", ":9879"), "Address to expose metrics on.")
		metricsPath = flag.String("web.telemetry-path", env("QBIT_EXPORTER_PATH", "/metrics"), "Path under which to expose metrics.")
		qbitURL     = flag.String("qbit.url", env("QBIT_URL", "http://localhost:8080"), "Base URL of the qBittorrent WebUI.")
		qbitUser    = flag.String("qbit.username", env("QBIT_USERNAME", ""), "qBittorrent WebUI username (empty if localhost auth is bypassed).")
		qbitPass    = flag.String("qbit.password", env("QBIT_PASSWORD", ""), "qBittorrent WebUI password.")
		timeout     = flag.Duration("qbit.timeout", envDuration("QBIT_TIMEOUT", 10*time.Second), "Per-scrape timeout.")
		tlsInsecure = flag.Bool("qbit.tls-insecure", envBool("QBIT_TLS_INSECURE", false), "Skip TLS certificate verification (self-signed HTTPS).")
		perTorrent  = flag.Bool("per-torrent", envBool("QBIT_PER_TORRENT", false), "Export per-torrent metrics. Off by default to keep cardinality low.")
		showVersion = flag.Bool("version", false, "Print version and exit.")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("qbit_exporter %s\n", version)
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	client, err := NewClient(*qbitURL, *qbitUser, *qbitPass, *timeout, *tlsInsecure)
	if err != nil {
		logger.Error("failed to create client", "err", err)
		os.Exit(1)
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(NewCollector(client, *timeout, *perTorrent, logger))

	mux := http.NewServeMux()
	mux.Handle(*metricsPath, promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `<html>
<head><title>qBittorrent Exporter</title></head>
<body>
<h1>qBittorrent Exporter</h1>
<p>Version %s</p>
<p><a href=%q>Metrics</a></p>
</body>
</html>`, version, *metricsPath)
	})

	srv := &http.Server{
		Addr:              *listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	logger.Info("starting qbit_exporter",
		"version", version,
		"listen", *listenAddr,
		"path", *metricsPath,
		"target", *qbitURL,
		"per_torrent", *perTorrent,
	)
	if err := srv.ListenAndServe(); err != nil {
		logger.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
