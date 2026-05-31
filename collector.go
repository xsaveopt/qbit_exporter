package main

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const namespace = "qbittorrent"

type Collector struct {
	client     *Client
	timeout    time.Duration
	perTorrent bool
	logger     *slog.Logger

	up             *prometheus.Desc
	scrapeDuration *prometheus.Desc

	appInfo    *prometheus.Desc
	connStatus *prometheus.Desc

	dlSpeed         *prometheus.Desc
	upSpeed         *prometheus.Desc
	dlData          *prometheus.Desc
	upData          *prometheus.Desc
	dlLimit         *prometheus.Desc
	upLimit         *prometheus.Desc
	alltimeDL       *prometheus.Desc
	alltimeUL       *prometheus.Desc
	globalRatio     *prometheus.Desc
	dhtNodes        *prometheus.Desc
	peerConnections *prometheus.Desc
	readCacheHits   *prometheus.Desc
	readCacheOver   *prometheus.Desc
	writeCacheOver  *prometheus.Desc
	buffersSize     *prometheus.Desc
	queuedSize      *prometheus.Desc
	queuedIOJobs    *prometheus.Desc
	avgQueueTime    *prometheus.Desc
	freeSpace       *prometheus.Desc
	wastedSession   *prometheus.Desc
	altSpeedLimits  *prometheus.Desc

	torrentsTotal *prometheus.Desc
	byState       *prometheus.Desc
	byCategory    *prometheus.Desc
	catDlSpeed    *prometheus.Desc
	catUpSpeed    *prometheus.Desc
	catSize       *prometheus.Desc

	tSize       *prometheus.Desc
	tProgress   *prometheus.Desc
	tDlSpeed    *prometheus.Desc
	tUpSpeed    *prometheus.Desc
	tRatio      *prometheus.Desc
	tDownloaded *prometheus.Desc
	tUploaded   *prometheus.Desc
	tAmountLeft *prometheus.Desc
	tSeeds      *prometheus.Desc
	tLeechs     *prometheus.Desc
	tETA        *prometheus.Desc
	tAddedOn    *prometheus.Desc
	tTimeActive *prometheus.Desc
}

func g(name, help string, labels ...string) *prometheus.Desc {
	return prometheus.NewDesc(prometheus.BuildFQName(namespace, "", name), help, labels, nil)
}

func NewCollector(client *Client, timeout time.Duration, perTorrent bool, logger *slog.Logger) *Collector {
	torrentLabels := []string{"hash", "name", "category", "state"}
	return &Collector{
		client:     client,
		timeout:    timeout,
		perTorrent: perTorrent,
		logger:     logger,

		up:             g("up", "Whether the last scrape of qBittorrent succeeded (1) or not (0)."),
		scrapeDuration: g("scrape_duration_seconds", "Duration of the qBittorrent scrape in seconds."),

		appInfo:    g("app_info", "qBittorrent build information; constant 1.", "version", "api_version", "qt", "libtorrent", "boost", "openssl"),
		connStatus: g("connection_status", "Current BitTorrent connection status; constant 1 with the status as a label.", "status"),

		dlSpeed:         g("dl_speed_bytes", "Global download rate in bytes per second."),
		upSpeed:         g("up_speed_bytes", "Global upload rate in bytes per second."),
		dlData:          g("session_downloaded_bytes", "Data downloaded this session in bytes."),
		upData:          g("session_uploaded_bytes", "Data uploaded this session in bytes."),
		dlLimit:         g("dl_rate_limit_bytes", "Global download rate limit in bytes per second (0 = unlimited)."),
		upLimit:         g("up_rate_limit_bytes", "Global upload rate limit in bytes per second (0 = unlimited)."),
		alltimeDL:       g("alltime_downloaded_bytes", "All-time downloaded data in bytes."),
		alltimeUL:       g("alltime_uploaded_bytes", "All-time uploaded data in bytes."),
		globalRatio:     g("global_ratio", "Global share ratio."),
		dhtNodes:        g("dht_nodes", "Number of nodes in the DHT."),
		peerConnections: g("peer_connections", "Total number of peer connections."),
		readCacheHits:   g("read_cache_hits_ratio", "Disk read cache hit ratio (0-1)."),
		readCacheOver:   g("read_cache_overload_ratio", "Read cache overload ratio (0-1)."),
		writeCacheOver:  g("write_cache_overload_ratio", "Write cache overload ratio (0-1)."),
		buffersSize:     g("total_buffers_size_bytes", "Total size of in-memory disk buffers in bytes."),
		queuedSize:      g("total_queued_size_bytes", "Total size of queued disk I/O in bytes."),
		queuedIOJobs:    g("queued_io_jobs", "Number of queued disk I/O jobs."),
		avgQueueTime:    g("average_queue_time_seconds", "Average disk I/O queue time in seconds."),
		freeSpace:       g("free_space_on_disk_bytes", "Free space on the default save path disk in bytes."),
		wastedSession:   g("session_wasted_bytes", "Data wasted (discarded) this session in bytes."),
		altSpeedLimits:  g("alt_speed_limits_enabled", "Whether alternative speed limits are active (1) or not (0)."),

		torrentsTotal: g("torrents_total", "Total number of torrents."),
		byState:       g("torrents_state_count", "Number of torrents in each state.", "state"),
		byCategory:    g("torrents_category_count", "Number of torrents in each category.", "category"),
		catDlSpeed:    g("category_dl_speed_bytes", "Aggregate download rate per category in bytes per second.", "category"),
		catUpSpeed:    g("category_up_speed_bytes", "Aggregate upload rate per category in bytes per second.", "category"),
		catSize:       g("category_size_bytes", "Aggregate size per category in bytes.", "category"),

		tSize:       g("torrent_size_bytes", "Torrent total size in bytes.", torrentLabels...),
		tProgress:   g("torrent_progress_ratio", "Torrent download progress (0-1).", torrentLabels...),
		tDlSpeed:    g("torrent_dl_speed_bytes", "Torrent download rate in bytes per second.", torrentLabels...),
		tUpSpeed:    g("torrent_up_speed_bytes", "Torrent upload rate in bytes per second.", torrentLabels...),
		tRatio:      g("torrent_ratio", "Torrent share ratio.", torrentLabels...),
		tDownloaded: g("torrent_downloaded_bytes", "Total data downloaded for the torrent in bytes.", torrentLabels...),
		tUploaded:   g("torrent_uploaded_bytes", "Total data uploaded for the torrent in bytes.", torrentLabels...),
		tAmountLeft: g("torrent_amount_left_bytes", "Data remaining to download in bytes.", torrentLabels...),
		tSeeds:      g("torrent_connected_seeds", "Number of connected seeds.", torrentLabels...),
		tLeechs:     g("torrent_connected_leechs", "Number of connected leechers.", torrentLabels...),
		tETA:        g("torrent_eta_seconds", "Estimated time to completion in seconds (8640000 = infinity).", torrentLabels...),
		tAddedOn:    g("torrent_added_timestamp_seconds", "Unix timestamp the torrent was added.", torrentLabels...),
		tTimeActive: g("torrent_time_active_seconds", "Total active time in seconds.", torrentLabels...),
	}
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.up
	ch <- c.scrapeDuration
}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	start := time.Now()
	snap, err := c.client.Scrape(ctx)
	dur := time.Since(start).Seconds()
	ch <- prometheus.MustNewConstMetric(c.scrapeDuration, prometheus.GaugeValue, dur)

	if err != nil {
		c.logger.Error("scrape failed", "err", err)
		ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, 1)

	c.collectApp(ch, snap)
	c.collectServer(ch, snap.Server)
	c.collectTorrents(ch, snap.Torrents)
}

func (c *Collector) collectApp(ch chan<- prometheus.Metric, snap *Snapshot) {
	b := snap.Build
	ch <- prometheus.MustNewConstMetric(c.appInfo, prometheus.GaugeValue, 1,
		snap.Version, snap.APIVersion, b.Qt, b.Libtorrent, b.Boost, b.OpenSSL)
	if snap.Server.ConnectionStatus != "" {
		ch <- prometheus.MustNewConstMetric(c.connStatus, prometheus.GaugeValue, 1, snap.Server.ConnectionStatus)
	}
}

func (c *Collector) collectServer(ch chan<- prometheus.Metric, s ServerState) {
	gauge := func(d *prometheus.Desc, v float64) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v)
	}
	gauge(c.dlSpeed, float64(s.DlInfoSpeed))
	gauge(c.upSpeed, float64(s.UpInfoSpeed))
	gauge(c.dlData, float64(s.DlInfoData))
	gauge(c.upData, float64(s.UpInfoData))
	gauge(c.dlLimit, float64(s.DlRateLimit))
	gauge(c.upLimit, float64(s.UpRateLimit))
	gauge(c.alltimeDL, float64(s.AlltimeDL))
	gauge(c.alltimeUL, float64(s.AlltimeUL))
	gauge(c.globalRatio, parseFloat(s.GlobalRatio))
	gauge(c.dhtNodes, float64(s.DHTNodes))
	gauge(c.peerConnections, float64(s.TotalPeerConnections))
	gauge(c.readCacheHits, parsePercent(s.ReadCacheHits))
	gauge(c.readCacheOver, parsePercent(s.ReadCacheOverload))
	gauge(c.writeCacheOver, parsePercent(s.WriteCacheOverload))
	gauge(c.buffersSize, float64(s.TotalBuffersSize))
	gauge(c.queuedSize, float64(s.TotalQueuedSize))
	gauge(c.queuedIOJobs, float64(s.QueuedIOJobs))
	gauge(c.avgQueueTime, float64(s.AverageTimeQueue)/1000.0)
	gauge(c.freeSpace, float64(s.FreeSpaceOnDisk))
	gauge(c.wastedSession, float64(s.TotalWastedSession))
	gauge(c.altSpeedLimits, boolToFloat(s.UseAltSpeedLimits))
}

func (c *Collector) collectTorrents(ch chan<- prometheus.Metric, torrents []Torrent) {
	ch <- prometheus.MustNewConstMetric(c.torrentsTotal, prometheus.GaugeValue, float64(len(torrents)))

	type catAgg struct {
		count            int
		dlSpeed, upSpeed int64
		size             int64
	}
	stateCount := map[string]int{}
	catAggs := map[string]*catAgg{}

	for _, t := range torrents {
		stateCount[t.State]++

		cat := t.Category
		if cat == "" {
			cat = "uncategorized"
		}
		a := catAggs[cat]
		if a == nil {
			a = &catAgg{}
			catAggs[cat] = a
		}
		a.count++
		a.dlSpeed += t.DlSpeed
		a.upSpeed += t.UpSpeed
		a.size += t.Size

		if c.perTorrent {
			c.collectOneTorrent(ch, t)
		}
	}

	for state, n := range stateCount {
		ch <- prometheus.MustNewConstMetric(c.byState, prometheus.GaugeValue, float64(n), state)
	}
	for cat, a := range catAggs {
		ch <- prometheus.MustNewConstMetric(c.byCategory, prometheus.GaugeValue, float64(a.count), cat)
		ch <- prometheus.MustNewConstMetric(c.catDlSpeed, prometheus.GaugeValue, float64(a.dlSpeed), cat)
		ch <- prometheus.MustNewConstMetric(c.catUpSpeed, prometheus.GaugeValue, float64(a.upSpeed), cat)
		ch <- prometheus.MustNewConstMetric(c.catSize, prometheus.GaugeValue, float64(a.size), cat)
	}
}

func (c *Collector) collectOneTorrent(ch chan<- prometheus.Metric, t Torrent) {
	lv := []string{t.Hash, t.Name, t.Category, t.State}
	m := func(d *prometheus.Desc, v float64) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, lv...)
	}
	m(c.tSize, float64(t.Size))
	m(c.tProgress, t.Progress)
	m(c.tDlSpeed, float64(t.DlSpeed))
	m(c.tUpSpeed, float64(t.UpSpeed))
	m(c.tRatio, t.Ratio)
	m(c.tDownloaded, float64(t.Downloaded))
	m(c.tUploaded, float64(t.Uploaded))
	m(c.tAmountLeft, float64(t.AmountLeft))
	m(c.tSeeds, float64(t.NumSeeds))
	m(c.tLeechs, float64(t.NumLeechs))
	m(c.tETA, float64(t.ETA))
	m(c.tAddedOn, float64(t.AddedOn))
	m(c.tTimeActive, float64(t.TimeActive))
}

func parseFloat(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

func parsePercent(s string) float64 {
	return parseFloat(s) / 100.0
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
