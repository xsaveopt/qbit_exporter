package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, baseURL, user, pass string) *Client {
	t.Helper()
	c, err := NewClient(baseURL, user, pass, 5*time.Second, false)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestNewClient(t *testing.T) {
	t.Run("trims trailing slashes and records credentials", func(t *testing.T) {
		c, err := NewClient("http://localhost:8080///", "admin", "secret", 3*time.Second, false)
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		if c.baseURL != "http://localhost:8080" {
			t.Errorf("baseURL = %q, want %q", c.baseURL, "http://localhost:8080")
		}
		if c.username != "admin" || c.password != "secret" {
			t.Errorf("credentials = %q/%q", c.username, c.password)
		}
		if !c.needLogin {
			t.Error("needLogin = false, want true when a username is set")
		}
		if c.loggedIn {
			t.Error("loggedIn = true before any login")
		}
		if c.http.Jar == nil {
			t.Error("http.Jar = nil, want a cookie jar")
		}
		if c.http.Timeout != 3*time.Second {
			t.Errorf("timeout = %v, want 3s", c.http.Timeout)
		}
	})

	t.Run("no username means no login", func(t *testing.T) {
		c := testClient(t, "http://localhost:8080", "", "")
		if c.needLogin {
			t.Error("needLogin = true, want false with an empty username")
		}
	})

	t.Run("insecure TLS", func(t *testing.T) {
		for _, insecure := range []bool{false, true} {
			c, err := NewClient("http://localhost:8080", "", "", time.Second, insecure)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			tr, ok := c.http.Transport.(*http.Transport)
			if !ok {
				t.Fatalf("transport type = %T, want *http.Transport", c.http.Transport)
			}
			if got := tr.TLSClientConfig.InsecureSkipVerify; got != insecure {
				t.Errorf("InsecureSkipVerify = %v, want %v", got, insecure)
			}
			if tr == http.DefaultTransport {
				t.Error("transport is the shared DefaultTransport, want a clone")
			}
		}
	})
}

func TestLogin(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{name: "ok", status: http.StatusOK, body: "Ok."},
		{name: "ok with trailing newline", status: http.StatusOK, body: "Ok.\n"},
		{name: "no content", status: http.StatusNoContent},
		{name: "banned", status: http.StatusForbidden, body: "Your IP address has been banned", wantErr: "login forbidden"},
		{name: "bad credentials", status: http.StatusOK, body: "Fails.", wantErr: "login failed (status 200): Fails."},
		{name: "server error", status: http.StatusInternalServerError, body: "boom", wantErr: "login failed (status 500): boom"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotForm string
			var gotReferer, gotContentType string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v2/auth/login" {
					t.Errorf("path = %q", r.URL.Path)
				}
				if r.Method != http.MethodPost {
					t.Errorf("method = %q, want POST", r.Method)
				}
				body, _ := io.ReadAll(r.Body)
				gotForm = string(body)
				gotReferer = r.Header.Get("Referer")
				gotContentType = r.Header.Get("Content-Type")
				w.WriteHeader(tt.status)
				if tt.body != "" {
					_, _ = io.WriteString(w, tt.body)
				}
			}))
			defer srv.Close()

			c := testClient(t, srv.URL, "admin", "p@ss word")
			err := c.login(context.Background())

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("login: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("login succeeded, want error containing %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
				}
			}

			if gotForm != "password=p%40ss+word&username=admin" {
				t.Errorf("form = %q", gotForm)
			}
			if gotReferer != srv.URL {
				t.Errorf("Referer = %q, want %q", gotReferer, srv.URL)
			}
			if gotContentType != "application/x-www-form-urlencoded" {
				t.Errorf("Content-Type = %q", gotContentType)
			}
		})
	}
}

func TestLoginRequestErrors(t *testing.T) {
	t.Run("bad base url", func(t *testing.T) {
		c := testClient(t, "http://\ninvalid", "admin", "pw")
		if err := c.login(context.Background()); err == nil {
			t.Fatal("login succeeded, want a request construction error")
		}
	})

	t.Run("unreachable server", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := srv.URL
		srv.Close()

		c := testClient(t, url, "admin", "pw")
		err := c.login(context.Background())
		if err == nil {
			t.Fatal("login succeeded against a closed server")
		}
		if !strings.Contains(err.Error(), "login request") {
			t.Errorf("error = %q, want it to mention the login request", err)
		}
	})
}

func TestEnsureLogin(t *testing.T) {
	t.Run("skipped without a username", func(t *testing.T) {
		var logins atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			logins.Add(1)
		}))
		defer srv.Close()

		c := testClient(t, srv.URL, "", "")
		if err := c.ensureLogin(context.Background()); err != nil {
			t.Fatalf("ensureLogin: %v", err)
		}
		if n := logins.Load(); n != 0 {
			t.Errorf("requests = %d, want 0", n)
		}
		if c.loggedIn {
			t.Error("loggedIn = true, want false when login is not needed")
		}
	})

	t.Run("logs in once and caches", func(t *testing.T) {
		var logins atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			logins.Add(1)
			_, _ = io.WriteString(w, "Ok.")
		}))
		defer srv.Close()

		c := testClient(t, srv.URL, "admin", "pw")
		for range 3 {
			if err := c.ensureLogin(context.Background()); err != nil {
				t.Fatalf("ensureLogin: %v", err)
			}
		}
		if n := logins.Load(); n != 1 {
			t.Errorf("logins = %d, want 1", n)
		}
		if !c.loggedIn {
			t.Error("loggedIn = false after a successful login")
		}
	})

	t.Run("failure leaves the client logged out", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()

		c := testClient(t, srv.URL, "admin", "pw")
		if err := c.ensureLogin(context.Background()); err == nil {
			t.Fatal("ensureLogin succeeded, want an error")
		}
		if c.loggedIn {
			t.Error("loggedIn = true after a failed login")
		}
	})

	t.Run("concurrent callers log in once", func(t *testing.T) {
		var logins atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			logins.Add(1)
			time.Sleep(5 * time.Millisecond)
			_, _ = io.WriteString(w, "Ok.")
		}))
		defer srv.Close()

		c := testClient(t, srv.URL, "admin", "pw")
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := c.ensureLogin(context.Background()); err != nil {
					t.Errorf("ensureLogin: %v", err)
				}
			}()
		}
		wg.Wait()
		if n := logins.Load(); n != 1 {
			t.Errorf("logins = %d, want 1", n)
		}
	})
}

func TestInvalidate(t *testing.T) {
	var logins atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		logins.Add(1)
		_, _ = io.WriteString(w, "Ok.")
	}))
	defer srv.Close()

	c := testClient(t, srv.URL, "admin", "pw")
	if err := c.ensureLogin(context.Background()); err != nil {
		t.Fatalf("ensureLogin: %v", err)
	}
	c.invalidate()
	if c.loggedIn {
		t.Error("loggedIn = true after invalidate")
	}
	if err := c.ensureLogin(context.Background()); err != nil {
		t.Fatalf("ensureLogin: %v", err)
	}
	if n := logins.Load(); n != 2 {
		t.Errorf("logins = %d, want 2", n)
	}
}

type sessionServer struct {
	*httptest.Server

	mu      sync.Mutex
	current string
	logins  int
	calls   int
	cookies []string

	forbidFirstCall bool
	handle          func(w http.ResponseWriter, r *http.Request)
}

func newSessionServer(t *testing.T, handle func(w http.ResponseWriter, r *http.Request)) *sessionServer {
	t.Helper()
	s := &sessionServer{handle: handle}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			s.mu.Lock()
			s.logins++
			s.current = fmt.Sprintf("sid-%d", s.logins)
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: s.current, Path: "/"})
			s.mu.Unlock()
			_, _ = io.WriteString(w, "Ok.")
			return
		}

		s.mu.Lock()
		s.calls++
		call := s.calls
		seen := ""
		if ck, err := r.Cookie("SID"); err == nil {
			seen = ck.Value
		}
		s.cookies = append(s.cookies, seen)
		stale := seen != s.current
		forbid := stale || (s.forbidFirstCall && call == 1)
		s.mu.Unlock()

		if forbid {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		s.handle(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *sessionServer) counts() (logins, calls int, cookies []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logins, s.calls, append([]string(nil), s.cookies...)
}

func TestGetJSONReloginAfterForbidden(t *testing.T) {
	srv := newSessionServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"name":"after-relogin"}`)
	})
	srv.forbidFirstCall = true

	c := testClient(t, srv.URL, "admin", "pw")
	var out struct {
		Name string `json:"name"`
	}
	if err := c.getJSON(context.Background(), "/api/v2/app/buildInfo", &out); err != nil {
		t.Fatalf("getJSON: %v", err)
	}
	if out.Name != "after-relogin" {
		t.Errorf("Name = %q, want %q", out.Name, "after-relogin")
	}

	logins, calls, cookies := srv.counts()
	if logins != 2 {
		t.Errorf("logins = %d, want 2", logins)
	}
	if calls != 2 {
		t.Errorf("data calls = %d, want 2", calls)
	}
	want := []string{"sid-1", "sid-2"}
	if len(cookies) != len(want) {
		t.Fatalf("cookies = %v, want %v", cookies, want)
	}
	for i := range want {
		if cookies[i] != want[i] {
			t.Errorf("cookie %d = %q, want %q", i, cookies[i], want[i])
		}
	}
	if !c.loggedIn {
		t.Error("loggedIn = false after a successful re-login")
	}
}

func TestGetJSONForbiddenWithoutLogin(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := testClient(t, srv.URL, "", "")
	err := c.getJSON(context.Background(), "/api/v2/app/buildInfo", &struct{}{})
	if err == nil {
		t.Fatal("getJSON succeeded, want a status error")
	}
	if !strings.Contains(err.Error(), "status 403") {
		t.Errorf("error = %q, want it to mention status 403", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("calls = %d, want 1 (no retry without credentials)", n)
	}
}

func TestGetJSON(t *testing.T) {
	t.Run("decodes into out", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"qt":"6.7.2","bitness":64}`)
		}))
		defer srv.Close()

		var b BuildInfo
		c := testClient(t, srv.URL, "", "")
		if err := c.getJSON(context.Background(), "/api/v2/app/buildInfo", &b); err != nil {
			t.Fatalf("getJSON: %v", err)
		}
		if b.Qt != "6.7.2" || b.Bitness != 64 {
			t.Errorf("BuildInfo = %+v", b)
		}
	})

	t.Run("nil out skips decoding", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `not json at all`)
		}))
		defer srv.Close()

		c := testClient(t, srv.URL, "", "")
		if err := c.getJSON(context.Background(), "/anything", nil); err != nil {
			t.Fatalf("getJSON: %v", err)
		}
	})

	t.Run("bad json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"qt":`)
		}))
		defer srv.Close()

		var b BuildInfo
		c := testClient(t, srv.URL, "", "")
		err := c.getJSON(context.Background(), "/api/v2/app/buildInfo", &b)
		if err == nil {
			t.Fatal("getJSON succeeded, want a decode error")
		}
		if !strings.Contains(err.Error(), "decode /api/v2/app/buildInfo") {
			t.Errorf("error = %q", err)
		}
	})

	t.Run("non-200", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		c := testClient(t, srv.URL, "", "")
		err := c.getJSON(context.Background(), "/nope", nil)
		if err == nil || !strings.Contains(err.Error(), "GET /nope: status 404") {
			t.Errorf("error = %v, want a 404 status error", err)
		}
	})

	t.Run("login failure short-circuits", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()

		c := testClient(t, srv.URL, "admin", "pw")
		err := c.getJSON(context.Background(), "/api/v2/app/buildInfo", nil)
		if err == nil || !strings.Contains(err.Error(), "login forbidden") {
			t.Errorf("error = %v, want the login failure", err)
		}
	})
}

func TestRawGet(t *testing.T) {
	t.Run("returns body and status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.RequestURI() != "/api/v2/sync/maindata?rid=0" {
				t.Errorf("request URI = %q", r.URL.RequestURI())
			}
			w.WriteHeader(http.StatusTeapot)
			_, _ = io.WriteString(w, "hello")
		}))
		defer srv.Close()

		c := testClient(t, srv.URL, "", "")
		body, status, err := c.rawGet(context.Background(), "/api/v2/sync/maindata?rid=0")
		if err != nil {
			t.Fatalf("rawGet: %v", err)
		}
		if string(body) != "hello" {
			t.Errorf("body = %q, want %q", body, "hello")
		}
		if status != http.StatusTeapot {
			t.Errorf("status = %d, want %d", status, http.StatusTeapot)
		}
	})

	t.Run("bad url", func(t *testing.T) {
		c := testClient(t, "http://\ninvalid", "", "")
		_, status, err := c.rawGet(context.Background(), "/x")
		if err == nil {
			t.Fatal("rawGet succeeded, want a request construction error")
		}
		if status != 0 {
			t.Errorf("status = %d, want 0", status)
		}
	})

	t.Run("transport error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := srv.URL
		srv.Close()

		c := testClient(t, url, "", "")
		_, _, err := c.rawGet(context.Background(), "/x")
		if err == nil || !strings.Contains(err.Error(), "GET /x") {
			t.Errorf("error = %v, want a transport error naming the path", err)
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		defer srv.Close()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		c := testClient(t, srv.URL, "", "")
		if _, _, err := c.rawGet(ctx, "/x"); err == nil {
			t.Fatal("rawGet succeeded with a cancelled context")
		}
	})
}

func TestGetString(t *testing.T) {
	t.Run("trims whitespace", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "  v5.0.4\n")
		}))
		defer srv.Close()

		c := testClient(t, srv.URL, "", "")
		got, err := c.getString(context.Background(), "/api/v2/app/version")
		if err != nil {
			t.Fatalf("getString: %v", err)
		}
		if got != "v5.0.4" {
			t.Errorf("version = %q, want %q", got, "v5.0.4")
		}
	})

	t.Run("re-logs in after a 403", func(t *testing.T) {
		srv := newSessionServer(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "2.11.2\n")
		})
		srv.forbidFirstCall = true

		c := testClient(t, srv.URL, "admin", "pw")
		got, err := c.getString(context.Background(), "/api/v2/app/webapiVersion")
		if err != nil {
			t.Fatalf("getString: %v", err)
		}
		if got != "2.11.2" {
			t.Errorf("api version = %q", got)
		}
		logins, calls, _ := srv.counts()
		if logins != 2 || calls != 2 {
			t.Errorf("logins = %d, calls = %d, want 2 and 2", logins, calls)
		}
	})

	t.Run("non-200", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()

		c := testClient(t, srv.URL, "", "")
		got, err := c.getString(context.Background(), "/api/v2/app/version")
		if err == nil || !strings.Contains(err.Error(), "status 503") {
			t.Errorf("error = %v, want a 503 status error", err)
		}
		if got != "" {
			t.Errorf("version = %q, want empty", got)
		}
	})

	t.Run("login failure short-circuits", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()

		c := testClient(t, srv.URL, "admin", "pw")
		if _, err := c.getString(context.Background(), "/api/v2/app/version"); err == nil {
			t.Fatal("getString succeeded, want the login failure")
		}
	})
}

func TestTrackers(t *testing.T) {
	t.Run("dedupes and drops pseudo trackers", func(t *testing.T) {
		var gotHash string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v2/torrents/trackers" {
				t.Errorf("path = %q", r.URL.Path)
			}
			gotHash = r.URL.Query().Get("hash")
			_, _ = io.WriteString(w, `[
				{"url":"** [DHT] **"},
				{"url":"** [PeX] **"},
				{"url":"** [LSD] **"},
				{"url":"https://tracker.example.org:443/announce"},
				{"url":"udp://tracker.example.org:6969/announce"},
				{"url":"http://other.example.net/announce"},
				{"url":"ws://ws.example.net:8000/announce"}
			]`)
		}))
		defer srv.Close()

		c := testClient(t, srv.URL, "", "")
		hosts, err := c.Trackers(context.Background(), "abc def/+&")
		if err != nil {
			t.Fatalf("Trackers: %v", err)
		}
		if gotHash != "abc def/+&" {
			t.Errorf("hash query = %q, want %q", gotHash, "abc def/+&")
		}
		want := []string{"tracker.example.org", "other.example.net", "ws.example.net"}
		if len(hosts) != len(want) {
			t.Fatalf("hosts = %v, want %v", hosts, want)
		}
		for i := range want {
			if hosts[i] != want[i] {
				t.Errorf("host %d = %q, want %q", i, hosts[i], want[i])
			}
		}
	})

	t.Run("empty list", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `[]`)
		}))
		defer srv.Close()

		c := testClient(t, srv.URL, "", "")
		hosts, err := c.Trackers(context.Background(), "hash")
		if err != nil {
			t.Fatalf("Trackers: %v", err)
		}
		if len(hosts) != 0 {
			t.Errorf("hosts = %v, want none", hosts)
		}
	})

	t.Run("propagates errors", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		c := testClient(t, srv.URL, "", "")
		if _, err := c.Trackers(context.Background(), "hash"); err == nil {
			t.Fatal("Trackers succeeded, want a status error")
		}
	})
}

func TestTrackerHost(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"http://tracker.example.com/announce", "tracker.example.com"},
		{"https://tracker.example.com:443/announce", "tracker.example.com"},
		{"udp://tracker.opentrackr.org:1337/announce", "tracker.opentrackr.org"},
		{"ws://ws.example.net:8000/announce", "ws.example.net"},
		{"wss://ws.example.net/announce", "ws.example.net"},
		{"udp://[2001:db8::1]:6969/announce", "2001:db8::1"},
		{"http://TRACKER.Example.COM/announce", "TRACKER.Example.COM"},
		{"** [DHT] **", ""},
		{"** [PeX] **", ""},
		{"** [LSD] **", ""},
		{"", ""},
		{"ftp://tracker.example.com/announce", ""},
		{"tracker.example.com:6969", ""},
		{"http://%zz/announce", ""},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			if got := trackerHost(tt.raw); got != tt.want {
				t.Errorf("trackerHost(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

const maindataJSON = `{
	"server_state": {
		"alltime_dl": 1000,
		"alltime_ul": 2000,
		"average_time_queue": 250,
		"connection_status": "connected",
		"dht_nodes": 321,
		"dl_info_data": 4096,
		"dl_info_speed": 512,
		"dl_rate_limit": 0,
		"free_space_on_disk": 123456789,
		"global_ratio": "2.00",
		"queued_io_jobs": 3,
		"read_cache_hits": "76.5",
		"read_cache_overload": "0",
		"total_buffers_size": 65536,
		"total_peer_connections": 42,
		"total_queued_size": 1024,
		"total_wasted_session": 17,
		"up_info_data": 8192,
		"up_info_speed": 256,
		"up_rate_limit": 1048576,
		"use_alt_speed_limits": true,
		"write_cache_overload": "0"
	},
	"torrents": {
		"aaaa": {
			"name": "alpha",
			"state": "uploading",
			"category": "movies",
			"size": 100,
			"progress": 1,
			"ratio": 2.5,
			"dlspeed": 0,
			"upspeed": 300,
			"downloaded": 100,
			"uploaded": 250,
			"amount_left": 0,
			"num_seeds": 1,
			"num_leechs": 2,
			"eta": 8640000,
			"added_on": 1700000000,
			"time_active": 3600
		},
		"bbbb": {
			"name": "beta",
			"state": "downloading",
			"category": "",
			"size": 200,
			"progress": 0.5,
			"ratio": 0,
			"dlspeed": 500,
			"upspeed": 0,
			"downloaded": 100,
			"uploaded": 0,
			"amount_left": 100,
			"num_seeds": 5,
			"num_leechs": 0,
			"eta": 120,
			"added_on": 1700000100,
			"time_active": 60
		}
	}
}`

func qbitHandler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/app/version":
			_, _ = io.WriteString(w, "v5.0.4\n")
		case "/api/v2/app/webapiVersion":
			_, _ = io.WriteString(w, "2.11.2")
		case "/api/v2/app/buildInfo":
			_, _ = io.WriteString(w, `{"qt":"6.7.2","libtorrent":"2.0.10.0","boost":"1.86.0","openssl":"3.3.2","bitness":64}`)
		case "/api/v2/sync/maindata":
			if r.URL.Query().Get("rid") != "0" {
				t.Errorf("rid = %q, want 0", r.URL.Query().Get("rid"))
			}
			_, _ = io.WriteString(w, maindataJSON)
		case "/api/v2/torrents/trackers":
			hash := r.URL.Query().Get("hash")
			_, _ = fmt.Fprintf(w, `[{"url":"** [DHT] **"},{"url":"https://tracker-%s.example.org/announce"}]`, hash)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func TestScrape(t *testing.T) {
	srv := httptest.NewServer(qbitHandler(t))
	defer srv.Close()

	c := testClient(t, srv.URL, "", "")
	snap, err := c.Scrape(context.Background())
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}

	if snap.Version != "v5.0.4" {
		t.Errorf("Version = %q", snap.Version)
	}
	if snap.APIVersion != "2.11.2" {
		t.Errorf("APIVersion = %q", snap.APIVersion)
	}
	if snap.Build.Qt != "6.7.2" || snap.Build.Libtorrent != "2.0.10.0" || snap.Build.Bitness != 64 {
		t.Errorf("Build = %+v", snap.Build)
	}
	if snap.Server.ConnectionStatus != "connected" || snap.Server.DHTNodes != 321 {
		t.Errorf("Server = %+v", snap.Server)
	}
	if !snap.Server.UseAltSpeedLimits {
		t.Error("UseAltSpeedLimits = false, want true")
	}
	if len(snap.Torrents) != 2 {
		t.Fatalf("torrents = %d, want 2", len(snap.Torrents))
	}

	byHash := map[string]Torrent{}
	for _, tr := range snap.Torrents {
		byHash[tr.Hash] = tr
	}
	alpha, ok := byHash["aaaa"]
	if !ok {
		t.Fatalf("torrent aaaa missing, got %v", byHash)
	}
	if alpha.Name != "alpha" || alpha.Category != "movies" || alpha.Ratio != 2.5 {
		t.Errorf("alpha = %+v", alpha)
	}
	beta, ok := byHash["bbbb"]
	if !ok {
		t.Fatalf("torrent bbbb missing, got %v", byHash)
	}
	if beta.Name != "beta" || beta.Category != "" || beta.Progress != 0.5 {
		t.Errorf("beta = %+v", beta)
	}
}

func TestScrapeErrors(t *testing.T) {
	failures := []string{
		"/api/v2/app/version",
		"/api/v2/app/webapiVersion",
		"/api/v2/app/buildInfo",
		"/api/v2/sync/maindata",
	}

	for _, fail := range failures {
		t.Run(fail, func(t *testing.T) {
			base := qbitHandler(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == fail {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				base(w, r)
			}))
			defer srv.Close()

			c := testClient(t, srv.URL, "", "")
			snap, err := c.Scrape(context.Background())
			if err == nil {
				t.Fatalf("Scrape succeeded, want an error from %s", fail)
			}
			if snap != nil {
				t.Errorf("snapshot = %+v, want nil", snap)
			}
			if !strings.Contains(err.Error(), fail) {
				t.Errorf("error = %q, want it to name %q", err, fail)
			}
		})
	}
}
