package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Client struct {
	baseURL  string
	username string
	password string
	http     *http.Client

	mu        sync.Mutex
	loggedIn  bool
	needLogin bool
}

func NewClient(baseURL, username, password string, timeout time.Duration, insecureTLS bool) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("cookie jar: %w", err)
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	if insecureTLS {
		transport.TLSClientConfig.InsecureSkipVerify = true
	}

	return &Client{
		baseURL:   strings.TrimRight(baseURL, "/"),
		username:  username,
		password:  password,
		needLogin: username != "",
		http: &http.Client{
			Jar:       jar,
			Timeout:   timeout,
			Transport: transport,
		},
	}, nil
}

func (c *Client) login(ctx context.Context) error {
	form := url.Values{}
	form.Set("username", c.username)
	form.Set("password", c.password)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", c.baseURL)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("login request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
	if resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("login forbidden (banned IP? too many failed attempts): %s", strings.TrimSpace(string(body)))
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "Ok." {
		return fmt.Errorf("login failed (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (c *Client) ensureLogin(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.needLogin || c.loggedIn {
		return nil
	}
	if err := c.login(ctx); err != nil {
		return err
	}
	c.loggedIn = true
	return nil
}

func (c *Client) invalidate() {
	c.mu.Lock()
	c.loggedIn = false
	c.mu.Unlock()
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	if err := c.ensureLogin(ctx); err != nil {
		return err
	}

	body, status, err := c.rawGet(ctx, path)
	if err != nil {
		return err
	}
	if status == http.StatusForbidden && c.needLogin {
		c.invalidate()
		if err := c.ensureLogin(ctx); err != nil {
			return err
		}
		if body, status, err = c.rawGet(ctx, path); err != nil {
			return err
		}
	}
	if status != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", path, status)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func (c *Client) rawGet(ctx context.Context, path string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read %s: %w", path, err)
	}
	return body, resp.StatusCode, nil
}

func (c *Client) getString(ctx context.Context, path string) (string, error) {
	if err := c.ensureLogin(ctx); err != nil {
		return "", err
	}
	body, status, err := c.rawGet(ctx, path)
	if err != nil {
		return "", err
	}
	if status == http.StatusForbidden && c.needLogin {
		c.invalidate()
		if err := c.ensureLogin(ctx); err != nil {
			return "", err
		}
		if body, status, err = c.rawGet(ctx, path); err != nil {
			return "", err
		}
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("GET %s: status %d", path, status)
	}
	return strings.TrimSpace(string(body)), nil
}

type BuildInfo struct {
	Qt         string `json:"qt"`
	Libtorrent string `json:"libtorrent"`
	Boost      string `json:"boost"`
	OpenSSL    string `json:"openssl"`
	Bitness    int    `json:"bitness"`
}

type ServerState struct {
	AlltimeDL            int64  `json:"alltime_dl"`
	AlltimeUL            int64  `json:"alltime_ul"`
	AverageTimeQueue     int64  `json:"average_time_queue"`
	ConnectionStatus     string `json:"connection_status"`
	DHTNodes             int64  `json:"dht_nodes"`
	DlInfoData           int64  `json:"dl_info_data"`
	DlInfoSpeed          int64  `json:"dl_info_speed"`
	DlRateLimit          int64  `json:"dl_rate_limit"`
	FreeSpaceOnDisk      int64  `json:"free_space_on_disk"`
	GlobalRatio          string `json:"global_ratio"`
	QueuedIOJobs         int64  `json:"queued_io_jobs"`
	ReadCacheHits        string `json:"read_cache_hits"`
	ReadCacheOverload    string `json:"read_cache_overload"`
	TotalBuffersSize     int64  `json:"total_buffers_size"`
	TotalPeerConnections int64  `json:"total_peer_connections"`
	TotalQueuedSize      int64  `json:"total_queued_size"`
	TotalWastedSession   int64  `json:"total_wasted_session"`
	UpInfoData           int64  `json:"up_info_data"`
	UpInfoSpeed          int64  `json:"up_info_speed"`
	UpRateLimit          int64  `json:"up_rate_limit"`
	UseAltSpeedLimits    bool   `json:"use_alt_speed_limits"`
	WriteCacheOverload   string `json:"write_cache_overload"`
}

type Torrent struct {
	Hash         string  `json:"-"`
	Name         string  `json:"name"`
	State        string  `json:"state"`
	Category     string  `json:"category"`
	Tags         string  `json:"tags"`
	Size         int64   `json:"size"`
	Completed    int64   `json:"completed"`
	AmountLeft   int64   `json:"amount_left"`
	Downloaded   int64   `json:"downloaded"`
	Uploaded     int64   `json:"uploaded"`
	Progress     float64 `json:"progress"`
	Ratio        float64 `json:"ratio"`
	DlSpeed      int64   `json:"dlspeed"`
	UpSpeed      int64   `json:"upspeed"`
	NumSeeds     int64   `json:"num_seeds"`
	NumLeechs    int64   `json:"num_leechs"`
	NumComplete  int64   `json:"num_complete"`
	NumIncomp    int64   `json:"num_incomplete"`
	ETA          int64   `json:"eta"`
	AddedOn      int64   `json:"added_on"`
	CompletionOn int64   `json:"completion_on"`
	LastActivity int64   `json:"last_activity"`
	TimeActive   int64   `json:"time_active"`
}

type mainData struct {
	ServerState ServerState        `json:"server_state"`
	Torrents    map[string]Torrent `json:"torrents"`
}

type Snapshot struct {
	Version    string
	APIVersion string
	Build      BuildInfo
	Server     ServerState
	Torrents   []Torrent
}

type trackerEntry struct {
	URL string `json:"url"`
}

func (c *Client) Trackers(ctx context.Context, hash string) ([]string, error) {
	var entries []trackerEntry
	if err := c.getJSON(ctx, "/api/v2/torrents/trackers?hash="+url.QueryEscape(hash), &entries); err != nil {
		return nil, err
	}
	hosts := make([]string, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		host := trackerHost(e.URL)
		if host == "" {
			continue
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		hosts = append(hosts, host)
	}
	return hosts, nil
}

func trackerHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	switch u.Scheme {
	case "http", "https", "udp", "ws", "wss":
		return u.Hostname()
	default:
		return ""
	}
}

func (c *Client) Scrape(ctx context.Context) (*Snapshot, error) {
	snap := &Snapshot{}

	var err error
	if snap.Version, err = c.getString(ctx, "/api/v2/app/version"); err != nil {
		return nil, err
	}
	if snap.APIVersion, err = c.getString(ctx, "/api/v2/app/webapiVersion"); err != nil {
		return nil, err
	}
	if err = c.getJSON(ctx, "/api/v2/app/buildInfo", &snap.Build); err != nil {
		return nil, err
	}

	var md mainData
	if err = c.getJSON(ctx, "/api/v2/sync/maindata?rid=0", &md); err != nil {
		return nil, err
	}
	snap.Server = md.ServerState
	snap.Torrents = make([]Torrent, 0, len(md.Torrents))
	for hash, t := range md.Torrents {
		t.Hash = hash
		snap.Torrents = append(snap.Torrents, t)
	}
	return snap, nil
}
