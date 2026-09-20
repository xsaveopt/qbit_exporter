package main

import (
	"context"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

type sample struct {
	name   string
	labels map[string]string
	value  float64
}

type funcCollector func(ch chan<- prometheus.Metric)

func (funcCollector) Describe(chan<- *prometheus.Desc) {}

func (f funcCollector) Collect(ch chan<- prometheus.Metric) { f(ch) }

func gatherSamples(t *testing.T, c prometheus.Collector) []sample {
	t.Helper()
	reg := prometheus.NewRegistry()
	if err := reg.Register(c); err != nil {
		t.Fatalf("register: %v", err)
	}
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var out []sample
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			s := sample{name: mf.GetName(), labels: map[string]string{}}
			for _, lp := range m.GetLabel() {
				s.labels[lp.GetName()] = lp.GetValue()
			}
			switch {
			case m.GetGauge() != nil:
				s.value = m.GetGauge().GetValue()
			case m.GetCounter() != nil:
				s.value = m.GetCounter().GetValue()
			}
			out = append(out, s)
		}
	}
	return out
}

func collectSamples(t *testing.T, collect func(ch chan<- prometheus.Metric)) []sample {
	t.Helper()
	return gatherSamples(t, funcCollector(collect))
}

func samplesNamed(samples []sample, name string) []sample {
	var out []sample
	for _, s := range samples {
		if s.name == name {
			out = append(out, s)
		}
	}
	return out
}

func oneSample(t *testing.T, samples []sample, name string) sample {
	t.Helper()
	got := samplesNamed(samples, name)
	if len(got) != 1 {
		t.Fatalf("%s: got %d samples, want exactly 1", name, len(got))
	}
	return got[0]
}

func wantValue(t *testing.T, samples []sample, name string, want float64) {
	t.Helper()
	got := oneSample(t, samples, name)
	if math.Abs(got.value-want) > 1e-9 {
		t.Errorf("%s = %v, want %v", name, got.value, want)
	}
}

func labelNames(s sample) []string {
	names := make([]string, 0, len(s.labels))
	for k := range s.labels {
		names = append(names, k)
	}
	slices.Sort(names)
	return names
}

func wantLabels(t *testing.T, s sample, want ...string) {
	t.Helper()
	slices.Sort(want)
	if got := labelNames(s); !slices.Equal(got, want) {
		t.Errorf("%s labels = %v, want %v", s.name, got, want)
	}
}

func metricNames(samples []sample) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range samples {
		if _, ok := seen[s.name]; ok {
			continue
		}
		seen[s.name] = struct{}{}
		out = append(out, s.name)
	}
	slices.Sort(out)
	return out
}

func newTestCollector(t *testing.T, perTorrent bool, store *Store) *Collector {
	t.Helper()
	srv := httptest.NewServer(qbitHandler(t))
	t.Cleanup(srv.Close)
	client := testClient(t, srv.URL, "", "")
	return NewCollector(client, store, 5*time.Second, perTorrent, time.Hour, discardLogger())
}

func TestParseFloat(t *testing.T) {
	tests := []struct {
		in   string
		want float64
	}{
		{in: "0", want: 0},
		{in: "1.23", want: 1.23},
		{in: "2.00", want: 2},
		{in: "-1", want: -1},
		{in: "1e3", want: 1000},
		{in: "  1.5", want: 0},
		{in: "1,5", want: 0},
		{in: "", want: 0},
		{in: "N/A", want: 0},
		{in: "∞", want: 0},
		{in: "76.5%", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := parseFloat(tt.in); got != tt.want {
				t.Errorf("parseFloat(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParsePercent(t *testing.T) {
	tests := []struct {
		in   string
		want float64
	}{
		{in: "0", want: 0},
		{in: "100", want: 1},
		{in: "76.5", want: 0.765},
		{in: "0.5", want: 0.005},
		{in: "-1", want: -0.01},
		{in: "", want: 0},
		{in: "N/A", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := parsePercent(tt.in)
			if math.Abs(got-tt.want) > 1e-12 {
				t.Errorf("parsePercent(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestBoolToFloat(t *testing.T) {
	if got := boolToFloat(true); got != 1 {
		t.Errorf("boolToFloat(true) = %v, want 1", got)
	}
	if got := boolToFloat(false); got != 0 {
		t.Errorf("boolToFloat(false) = %v, want 0", got)
	}
}

func TestCollectorDescribe(t *testing.T) {
	c := newTestCollector(t, false, nil)

	ch := make(chan *prometheus.Desc, 16)
	c.Describe(ch)
	close(ch)

	var got []string
	for d := range ch {
		got = append(got, d.String())
	}
	if len(got) != 2 {
		t.Fatalf("described %d descriptors, want 2", len(got))
	}
	for _, want := range []string{"qbittorrent_up", "qbittorrent_scrape_duration_seconds"} {
		found := false
		for _, d := range got {
			if strings.Contains(d, `fqName: "`+want+`"`) {
				found = true
			}
		}
		if !found {
			t.Errorf("descriptor for %s missing from %v", want, got)
		}
	}
}

func TestCollectorCollect(t *testing.T) {
	c := newTestCollector(t, false, nil)

	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := `
# HELP qbittorrent_up Whether the last scrape of qBittorrent succeeded (1) or not (0).
# TYPE qbittorrent_up gauge
qbittorrent_up 1
# HELP qbittorrent_torrents_total Total number of torrents.
# TYPE qbittorrent_torrents_total gauge
qbittorrent_torrents_total 2
# HELP qbittorrent_app_info qBittorrent build information; constant 1.
# TYPE qbittorrent_app_info gauge
qbittorrent_app_info{api_version="2.11.2",boost="1.86.0",libtorrent="2.0.10.0",openssl="3.3.2",qt="6.7.2",version="v5.0.4"} 1
# HELP qbittorrent_connection_status Current BitTorrent connection status; constant 1 with the status as a label.
# TYPE qbittorrent_connection_status gauge
qbittorrent_connection_status{status="connected"} 1
`
	err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"qbittorrent_up", "qbittorrent_torrents_total", "qbittorrent_app_info", "qbittorrent_connection_status")
	if err != nil {
		t.Error(err)
	}

	n, err := testutil.GatherAndCount(reg, "qbittorrent_scrape_duration_seconds")
	if err != nil {
		t.Fatalf("GatherAndCount: %v", err)
	}
	if n != 1 {
		t.Errorf("scrape duration samples = %d, want 1", n)
	}
}

func TestCollectorCollectScrapeFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewCollector(testClient(t, srv.URL, "", ""), nil, 5*time.Second, true, time.Hour, discardLogger())
	samples := gatherSamples(t, c)

	wantValue(t, samples, "qbittorrent_up", 0)
	if got := samplesNamed(samples, "qbittorrent_scrape_duration_seconds"); len(got) != 1 {
		t.Errorf("scrape duration samples = %d, want 1", len(got))
	}
	if names := metricNames(samples); len(names) != 2 {
		t.Errorf("metrics on a failed scrape = %v, want only up and scrape_duration_seconds", names)
	}
}

func TestCollectorCollectTimesOut(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-block
		w.WriteHeader(http.StatusOK)
	}))
	defer func() {
		close(block)
		srv.Close()
	}()

	c := NewCollector(testClient(t, srv.URL, "", ""), nil, 20*time.Millisecond, false, time.Hour, discardLogger())
	samples := gatherSamples(t, c)
	wantValue(t, samples, "qbittorrent_up", 0)
}

func TestCollectApp(t *testing.T) {
	c := newTestCollector(t, false, nil)

	t.Run("full snapshot", func(t *testing.T) {
		snap := &Snapshot{
			Version:    "v5.0.4",
			APIVersion: "2.11.2",
			Build:      BuildInfo{Qt: "6.7.2", Libtorrent: "2.0.10.0", Boost: "1.86.0", OpenSSL: "3.3.2", Bitness: 64},
			Server:     ServerState{ConnectionStatus: "firewalled"},
		}
		samples := collectSamples(t, func(ch chan<- prometheus.Metric) { c.collectApp(ch, snap) })

		info := oneSample(t, samples, "qbittorrent_app_info")
		wantLabels(t, info, "version", "api_version", "qt", "libtorrent", "boost", "openssl")
		if info.value != 1 {
			t.Errorf("app_info = %v, want 1", info.value)
		}
		want := map[string]string{
			"version":     "v5.0.4",
			"api_version": "2.11.2",
			"qt":          "6.7.2",
			"libtorrent":  "2.0.10.0",
			"boost":       "1.86.0",
			"openssl":     "3.3.2",
		}
		for k, v := range want {
			if info.labels[k] != v {
				t.Errorf("app_info label %s = %q, want %q", k, info.labels[k], v)
			}
		}

		status := oneSample(t, samples, "qbittorrent_connection_status")
		wantLabels(t, status, "status")
		if status.labels["status"] != "firewalled" {
			t.Errorf("status label = %q", status.labels["status"])
		}
	})

	t.Run("empty connection status is dropped", func(t *testing.T) {
		snap := &Snapshot{Version: "v5.0.4"}
		samples := collectSamples(t, func(ch chan<- prometheus.Metric) { c.collectApp(ch, snap) })
		if got := samplesNamed(samples, "qbittorrent_connection_status"); len(got) != 0 {
			t.Errorf("connection_status samples = %d, want 0", len(got))
		}
		if got := samplesNamed(samples, "qbittorrent_app_info"); len(got) != 1 {
			t.Errorf("app_info samples = %d, want 1", len(got))
		}
	})
}

func TestCollectServer(t *testing.T) {
	c := newTestCollector(t, false, nil)

	state := ServerState{
		AlltimeDL:            1000,
		AlltimeUL:            2000,
		AverageTimeQueue:     250,
		ConnectionStatus:     "connected",
		DHTNodes:             321,
		DlInfoData:           4096,
		DlInfoSpeed:          512,
		DlRateLimit:          0,
		FreeSpaceOnDisk:      123456789,
		GlobalRatio:          "2.00",
		QueuedIOJobs:         3,
		ReadCacheHits:        "76.5",
		ReadCacheOverload:    "0",
		TotalBuffersSize:     65536,
		TotalPeerConnections: 42,
		TotalQueuedSize:      1024,
		TotalWastedSession:   17,
		UpInfoData:           8192,
		UpInfoSpeed:          256,
		UpRateLimit:          1048576,
		UseAltSpeedLimits:    true,
		WriteCacheOverload:   "12.5",
	}
	samples := collectSamples(t, func(ch chan<- prometheus.Metric) { c.collectServer(ch, state) })

	want := map[string]float64{
		"qbittorrent_dl_speed_bytes":             512,
		"qbittorrent_up_speed_bytes":             256,
		"qbittorrent_session_downloaded_bytes":   4096,
		"qbittorrent_session_uploaded_bytes":     8192,
		"qbittorrent_dl_rate_limit_bytes":        0,
		"qbittorrent_up_rate_limit_bytes":        1048576,
		"qbittorrent_alltime_downloaded_bytes":   1000,
		"qbittorrent_alltime_uploaded_bytes":     2000,
		"qbittorrent_global_ratio":               2,
		"qbittorrent_dht_nodes":                  321,
		"qbittorrent_peer_connections":           42,
		"qbittorrent_read_cache_hits_ratio":      0.765,
		"qbittorrent_read_cache_overload_ratio":  0,
		"qbittorrent_write_cache_overload_ratio": 0.125,
		"qbittorrent_total_buffers_size_bytes":   65536,
		"qbittorrent_total_queued_size_bytes":    1024,
		"qbittorrent_queued_io_jobs":             3,
		"qbittorrent_average_queue_time_seconds": 0.25,
		"qbittorrent_free_space_on_disk_bytes":   123456789,
		"qbittorrent_session_wasted_bytes":       17,
		"qbittorrent_alt_speed_limits_enabled":   1,
	}
	for name, v := range want {
		wantValue(t, samples, name, v)
	}
	if len(samples) != len(want) {
		t.Errorf("collectServer emitted %d samples, want %d", len(samples), len(want))
	}
	for _, s := range samples {
		if len(s.labels) != 0 {
			t.Errorf("%s has labels %v, want none", s.name, labelNames(s))
		}
	}

	t.Run("alt speed limits off", func(t *testing.T) {
		off := state
		off.UseAltSpeedLimits = false
		samples := collectSamples(t, func(ch chan<- prometheus.Metric) { c.collectServer(ch, off) })
		wantValue(t, samples, "qbittorrent_alt_speed_limits_enabled", 0)
	})

	t.Run("unparseable strings become zero", func(t *testing.T) {
		bad := state
		bad.GlobalRatio = "N/A"
		bad.ReadCacheHits = ""
		bad.WriteCacheOverload = "n/a"
		samples := collectSamples(t, func(ch chan<- prometheus.Metric) { c.collectServer(ch, bad) })
		wantValue(t, samples, "qbittorrent_global_ratio", 0)
		wantValue(t, samples, "qbittorrent_read_cache_hits_ratio", 0)
		wantValue(t, samples, "qbittorrent_write_cache_overload_ratio", 0)
	})
}

func testTorrents() []Torrent {
	return []Torrent{
		{
			Hash: "aaaa", Name: "alpha", State: "uploading", Category: "movies",
			Size: 100, Progress: 1, Ratio: 2.5, DlSpeed: 0, UpSpeed: 300,
			Downloaded: 100, Uploaded: 250, AmountLeft: 0,
			NumSeeds: 1, NumLeechs: 2, ETA: 8640000, AddedOn: 1700000000, TimeActive: 3600,
		},
		{
			Hash: "bbbb", Name: "beta", State: "downloading", Category: "",
			Size: 200, Progress: 0.5, Ratio: 0, DlSpeed: 500, UpSpeed: 0,
			Downloaded: 100, Uploaded: 0, AmountLeft: 100,
			NumSeeds: 5, NumLeechs: 0, ETA: 120, AddedOn: 1700000100, TimeActive: 60,
		},
		{
			Hash: "cccc", Name: "gamma", State: "uploading", Category: "movies",
			Size: 300, Progress: 1, Ratio: 1.5, DlSpeed: 0, UpSpeed: 100,
			Downloaded: 300, Uploaded: 450, AmountLeft: 0,
			NumSeeds: 0, NumLeechs: 4, ETA: 8640000, AddedOn: 1700000200, TimeActive: 7200,
		},
	}
}

func TestCollectTorrents(t *testing.T) {
	t.Run("aggregates by state and category", func(t *testing.T) {
		c := newTestCollector(t, false, nil)
		samples := collectSamples(t, func(ch chan<- prometheus.Metric) {
			c.collectTorrents(ch, testTorrents())
		})

		wantValue(t, samples, "qbittorrent_torrents_total", 3)

		states := map[string]float64{}
		for _, s := range samplesNamed(samples, "qbittorrent_torrents_state_count") {
			wantLabels(t, s, "state")
			states[s.labels["state"]] = s.value
		}
		if states["uploading"] != 2 || states["downloading"] != 1 || len(states) != 2 {
			t.Errorf("state counts = %v", states)
		}

		counts := map[string]float64{}
		for _, s := range samplesNamed(samples, "qbittorrent_torrents_category_count") {
			wantLabels(t, s, "category")
			counts[s.labels["category"]] = s.value
		}
		if counts["movies"] != 2 || counts["uncategorized"] != 1 || len(counts) != 2 {
			t.Errorf("category counts = %v", counts)
		}

		agg := func(name string) map[string]float64 {
			out := map[string]float64{}
			for _, s := range samplesNamed(samples, name) {
				wantLabels(t, s, "category")
				out[s.labels["category"]] = s.value
			}
			return out
		}
		if dl := agg("qbittorrent_category_dl_speed_bytes"); dl["movies"] != 0 || dl["uncategorized"] != 500 {
			t.Errorf("category dl speed = %v", dl)
		}
		if ul := agg("qbittorrent_category_up_speed_bytes"); ul["movies"] != 400 || ul["uncategorized"] != 0 {
			t.Errorf("category up speed = %v", ul)
		}
		if size := agg("qbittorrent_category_size_bytes"); size["movies"] != 400 || size["uncategorized"] != 200 {
			t.Errorf("category size = %v", size)
		}
	})

	t.Run("no torrents", func(t *testing.T) {
		c := newTestCollector(t, true, nil)
		samples := collectSamples(t, func(ch chan<- prometheus.Metric) {
			c.collectTorrents(ch, nil)
		})
		wantValue(t, samples, "qbittorrent_torrents_total", 0)
		if len(samples) != 1 {
			t.Errorf("samples = %v, want only torrents_total", metricNames(samples))
		}
	})

	t.Run("per-torrent metrics are off by default", func(t *testing.T) {
		c := newTestCollector(t, false, nil)
		samples := collectSamples(t, func(ch chan<- prometheus.Metric) {
			c.collectTorrents(ch, testTorrents())
		})
		for _, name := range metricNames(samples) {
			if strings.HasPrefix(name, "qbittorrent_torrent_") {
				t.Errorf("per-torrent metric %s emitted with perTorrent off", name)
			}
		}
		if got := len(samples); got != 1+2+2*4 {
			t.Errorf("samples = %d, want %d", got, 1+2+2*4)
		}
	})

	t.Run("per-torrent metrics on", func(t *testing.T) {
		c := newTestCollector(t, true, nil)
		torrents := testTorrents()
		samples := collectSamples(t, func(ch chan<- prometheus.Metric) {
			c.collectTorrents(ch, torrents)
		})

		var perTorrent []sample
		for _, s := range samples {
			if strings.HasPrefix(s.name, "qbittorrent_torrent_") {
				perTorrent = append(perTorrent, s)
			}
		}
		if got, want := len(perTorrent), 13*len(torrents); got != want {
			t.Errorf("per-torrent samples = %d, want %d", got, want)
		}
		for _, s := range perTorrent {
			wantLabels(t, s, "hash", "name", "category", "state")
		}

		hashes := map[string]struct{}{}
		for _, s := range samplesNamed(samples, "qbittorrent_torrent_size_bytes") {
			hashes[s.labels["hash"]] = struct{}{}
		}
		if len(hashes) != len(torrents) {
			t.Errorf("distinct hashes = %d, want %d", len(hashes), len(torrents))
		}
	})

	t.Run("uncategorized label is not empty", func(t *testing.T) {
		c := newTestCollector(t, true, nil)
		samples := collectSamples(t, func(ch chan<- prometheus.Metric) {
			c.collectTorrents(ch, []Torrent{{Hash: "h", Name: "n", State: "pausedUP"}})
		})
		cat := oneSample(t, samples, "qbittorrent_torrents_category_count")
		if cat.labels["category"] != "uncategorized" {
			t.Errorf("category = %q, want uncategorized", cat.labels["category"])
		}
		size := oneSample(t, samples, "qbittorrent_torrent_size_bytes")
		if size.labels["category"] != "" {
			t.Errorf("per-torrent category = %q, want the raw empty value", size.labels["category"])
		}
	})
}

func TestCollectOneTorrent(t *testing.T) {
	c := newTestCollector(t, true, nil)
	tor := testTorrents()[0]

	samples := collectSamples(t, func(ch chan<- prometheus.Metric) { c.collectOneTorrent(ch, tor) })

	if len(samples) != 13 {
		t.Fatalf("samples = %v, want 13", metricNames(samples))
	}
	want := map[string]float64{
		"qbittorrent_torrent_size_bytes":              100,
		"qbittorrent_torrent_progress_ratio":          1,
		"qbittorrent_torrent_dl_speed_bytes":          0,
		"qbittorrent_torrent_up_speed_bytes":          300,
		"qbittorrent_torrent_ratio":                   2.5,
		"qbittorrent_torrent_downloaded_bytes":        100,
		"qbittorrent_torrent_uploaded_bytes":          250,
		"qbittorrent_torrent_amount_left_bytes":       0,
		"qbittorrent_torrent_connected_seeds":         1,
		"qbittorrent_torrent_connected_leechs":        2,
		"qbittorrent_torrent_eta_seconds":             8640000,
		"qbittorrent_torrent_added_timestamp_seconds": 1700000000,
		"qbittorrent_torrent_time_active_seconds":     3600,
	}
	for name, v := range want {
		wantValue(t, samples, name, v)
	}
	for _, s := range samples {
		wantLabels(t, s, "hash", "name", "category", "state")
		if s.labels["hash"] != "aaaa" || s.labels["name"] != "alpha" ||
			s.labels["category"] != "movies" || s.labels["state"] != "uploading" {
			t.Errorf("%s labels = %v", s.name, s.labels)
		}
	}
}

func TestCollectTrackers(t *testing.T) {
	t.Run("fetches, stores and reports", func(t *testing.T) {
		store := newStore(t)
		c := newTestCollector(t, false, store)
		torrents := testTorrents()

		samples := collectSamples(t, func(ch chan<- prometheus.Metric) {
			c.collectTrackers(context.Background(), ch, torrents)
		})

		names := metricNames(samples)
		wantNames := []string{
			"qbittorrent_tracker_downloaded_bytes",
			"qbittorrent_tracker_ratio",
			"qbittorrent_tracker_torrents_count",
			"qbittorrent_tracker_uploaded_bytes",
		}
		if !slices.Equal(names, wantNames) {
			t.Fatalf("metrics = %v, want %v", names, wantNames)
		}

		up := map[string]float64{}
		for _, s := range samplesNamed(samples, "qbittorrent_tracker_uploaded_bytes") {
			wantLabels(t, s, "tracker")
			up[s.labels["tracker"]] = s.value
		}
		if len(up) != len(torrents) {
			t.Fatalf("trackers = %v, want one per torrent", up)
		}
		if up["tracker-aaaa.example.org"] != 250 {
			t.Errorf("uploaded for aaaa = %v, want 250", up["tracker-aaaa.example.org"])
		}

		ratios := map[string]float64{}
		for _, s := range samplesNamed(samples, "qbittorrent_tracker_ratio") {
			ratios[s.labels["tracker"]] = s.value
		}
		if math.Abs(ratios["tracker-aaaa.example.org"]-2.5) > 1e-9 {
			t.Errorf("ratio for aaaa = %v, want 2.5", ratios["tracker-aaaa.example.org"])
		}
		if ratios["tracker-bbbb.example.org"] != 0 {
			t.Errorf("ratio for bbbb = %v, want 0 when nothing was uploaded", ratios["tracker-bbbb.example.org"])
		}

		for _, tor := range torrents {
			stale, err := store.TrackersStale(tor.Hash, time.Now().Unix(), int64(time.Hour.Seconds()))
			if err != nil {
				t.Fatalf("TrackersStale: %v", err)
			}
			if stale {
				t.Errorf("%s still stale after a refresh", tor.Hash)
			}
		}
	})

	t.Run("zero downloaded gives a zero ratio", func(t *testing.T) {
		store := newStore(t)
		c := newTestCollector(t, false, store)

		torrents := []Torrent{{Hash: "aaaa", Name: "alpha", Uploaded: 500, Downloaded: 0}}
		samples := collectSamples(t, func(ch chan<- prometheus.Metric) {
			c.collectTrackers(context.Background(), ch, torrents)
		})
		wantValue(t, samples, "qbittorrent_tracker_ratio", 0)
		wantValue(t, samples, "qbittorrent_tracker_uploaded_bytes", 500)
		wantValue(t, samples, "qbittorrent_tracker_downloaded_bytes", 0)
		wantValue(t, samples, "qbittorrent_tracker_torrents_count", 1)
	})

	t.Run("fresh trackers are not re-fetched", func(t *testing.T) {
		store := newStore(t)
		var fetches atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v2/torrents/trackers" {
				fetches.Add(1)
				_, _ = io.WriteString(w, `[{"url":"https://tracker.example.org/announce"}]`)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		c := NewCollector(testClient(t, srv.URL, "", ""), store, 5*time.Second, false, time.Hour, discardLogger())
		torrents := []Torrent{{Hash: "aaaa", Name: "alpha", Uploaded: 10, Downloaded: 5}}

		for range 3 {
			collectSamples(t, func(ch chan<- prometheus.Metric) {
				c.collectTrackers(context.Background(), ch, torrents)
			})
		}
		if n := fetches.Load(); n != 1 {
			t.Errorf("tracker fetches = %d, want 1 inside the refresh window", n)
		}
	})

	t.Run("expired refresh window re-fetches", func(t *testing.T) {
		store := newStore(t)
		var fetches atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v2/torrents/trackers" {
				fetches.Add(1)
				_, _ = io.WriteString(w, `[{"url":"https://tracker.example.org/announce"}]`)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		c := NewCollector(testClient(t, srv.URL, "", ""), store, 5*time.Second, false, 0, discardLogger())
		torrents := []Torrent{{Hash: "aaaa", Name: "alpha", Uploaded: 10, Downloaded: 5}}

		for range 3 {
			collectSamples(t, func(ch chan<- prometheus.Metric) {
				c.collectTrackers(context.Background(), ch, torrents)
			})
		}
		if n := fetches.Load(); n != 3 {
			t.Errorf("tracker fetches = %d, want 3 with a zero refresh window", n)
		}
	})

	t.Run("accumulates across scrapes", func(t *testing.T) {
		store := newStore(t)
		c := newTestCollector(t, false, store)

		first := []Torrent{{Hash: "aaaa", Name: "alpha", Uploaded: 100, Downloaded: 100}}
		collectSamples(t, func(ch chan<- prometheus.Metric) {
			c.collectTrackers(context.Background(), ch, first)
		})

		second := []Torrent{{Hash: "aaaa", Name: "alpha", Uploaded: 400, Downloaded: 200}}
		samples := collectSamples(t, func(ch chan<- prometheus.Metric) {
			c.collectTrackers(context.Background(), ch, second)
		})
		wantValue(t, samples, "qbittorrent_tracker_uploaded_bytes", 400)
		wantValue(t, samples, "qbittorrent_tracker_downloaded_bytes", 200)

		third := []Torrent{{Hash: "aaaa", Name: "alpha", Uploaded: 10, Downloaded: 10}}
		samples = collectSamples(t, func(ch chan<- prometheus.Metric) {
			c.collectTrackers(context.Background(), ch, third)
		})
		wantValue(t, samples, "qbittorrent_tracker_uploaded_bytes", 410)
		wantValue(t, samples, "qbittorrent_tracker_downloaded_bytes", 210)
	})

	t.Run("a closed store emits nothing", func(t *testing.T) {
		store, err := OpenStore(filepath.Join(t.TempDir(), "closed.db"))
		if err != nil {
			t.Fatalf("OpenStore: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		c := newTestCollector(t, false, store)
		samples := collectSamples(t, func(ch chan<- prometheus.Metric) {
			c.collectTrackers(context.Background(), ch, testTorrents())
		})
		if len(samples) != 0 {
			t.Errorf("samples = %v, want none", metricNames(samples))
		}
	})

	t.Run("a tracker fetch failure does not stop the rest", func(t *testing.T) {
		store := newStore(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("hash") == "bbbb" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = io.WriteString(w, `[{"url":"https://tracker.example.org/announce"}]`)
		}))
		defer srv.Close()

		c := NewCollector(testClient(t, srv.URL, "", ""), store, 5*time.Second, false, time.Hour, discardLogger())
		samples := collectSamples(t, func(ch chan<- prometheus.Metric) {
			c.collectTrackers(context.Background(), ch, testTorrents())
		})

		count := oneSample(t, samples, "qbittorrent_tracker_torrents_count")
		if count.value != 2 {
			t.Errorf("tracker torrents = %v, want 2 (bbbb failed to fetch)", count.value)
		}
	})
}

func TestRefreshTrackers(t *testing.T) {
	t.Run("no torrents makes no calls", func(t *testing.T) {
		store := newStore(t)
		var calls atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			calls.Add(1)
		}))
		defer srv.Close()

		c := NewCollector(testClient(t, srv.URL, "", ""), store, 5*time.Second, false, time.Hour, discardLogger())
		c.refreshTrackers(context.Background(), nil, 1)
		if n := calls.Load(); n != 0 {
			t.Errorf("calls = %d, want 0", n)
		}
	})

	t.Run("fetches more torrents than there are workers", func(t *testing.T) {
		store := newStore(t)
		var calls, concurrent, peak atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			n := concurrent.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			concurrent.Add(-1)
			_, _ = io.WriteString(w, `[{"url":"https://t-`+r.URL.Query().Get("hash")+`.example.org/announce"}]`)
		}))
		defer srv.Close()

		c := NewCollector(testClient(t, srv.URL, "", ""), store, 5*time.Second, false, time.Hour, discardLogger())

		const now = int64(1700000000)
		var torrents []Torrent
		for i := range trackerFetchWorkers * 3 {
			tor := Torrent{Hash: string(rune('a'+i)) + "hash", Name: "n"}
			if err := store.UpsertTorrent(tor.Hash, tor.Name, 0, 0, now); err != nil {
				t.Fatalf("UpsertTorrent: %v", err)
			}
			torrents = append(torrents, tor)
		}
		c.refreshTrackers(context.Background(), torrents, now)

		if n := calls.Load(); n != int64(len(torrents)) {
			t.Errorf("calls = %d, want %d", n, len(torrents))
		}
		if p := peak.Load(); p > trackerFetchWorkers {
			t.Errorf("peak concurrency = %d, want at most %d", p, trackerFetchWorkers)
		}

		for _, tor := range torrents {
			stale, err := store.TrackersStale(tor.Hash, now, 3600)
			if err != nil {
				t.Fatalf("TrackersStale: %v", err)
			}
			if stale {
				t.Errorf("%s not marked refreshed", tor.Hash)
			}
		}
	})

	t.Run("a cancelled context stops early", func(t *testing.T) {
		store := newStore(t)
		var calls atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			_, _ = io.WriteString(w, `[]`)
		}))
		defer srv.Close()

		c := NewCollector(testClient(t, srv.URL, "", ""), store, 5*time.Second, false, time.Hour, discardLogger())

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		var torrents []Torrent
		for i := range trackerFetchWorkers * 4 {
			torrents = append(torrents, Torrent{Hash: string(rune('a'+i)) + "hash", Name: "n"})
		}
		c.refreshTrackers(ctx, torrents, 1)

		if n := calls.Load(); n > int64(trackerFetchWorkers) {
			t.Errorf("calls = %d, want at most one batch of %d", n, trackerFetchWorkers)
		}
	})

	t.Run("fetch errors leave the torrent stale", func(t *testing.T) {
		store := newStore(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		const now = int64(1700000000)
		c := NewCollector(testClient(t, srv.URL, "", ""), store, 5*time.Second, false, time.Hour, discardLogger())
		if err := store.UpsertTorrent("aaaa", "alpha", 1, 1, now); err != nil {
			t.Fatalf("UpsertTorrent: %v", err)
		}
		c.refreshTrackers(context.Background(), []Torrent{{Hash: "aaaa", Name: "alpha"}}, now)

		stale, err := store.TrackersStale("aaaa", now, 3600)
		if err != nil {
			t.Fatalf("TrackersStale: %v", err)
		}
		if !stale {
			t.Error("stale = false, want the torrent left stale after a failed fetch")
		}
	})
}
