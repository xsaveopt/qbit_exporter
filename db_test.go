package main

import (
	"path/filepath"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "qbit.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func torrentRow(t *testing.T, s *Store, hash string) (name string, up, down, lastUp, lastDown, updated int64) {
	t.Helper()
	err := s.db.QueryRow(
		`SELECT name, uploaded, downloaded, last_uploaded, last_downloaded, updated_at FROM torrents WHERE hash = ?`,
		hash).Scan(&name, &up, &down, &lastUp, &lastDown, &updated)
	if err != nil {
		t.Fatalf("select torrent %s: %v", hash, err)
	}
	return name, up, down, lastUp, lastDown, updated
}

func TestOpenStore(t *testing.T) {
	t.Run("fresh database", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "fresh.db")

		s, err := OpenStore(path)
		if err != nil {
			t.Fatalf("OpenStore: %v", err)
		}
		defer func() { _ = s.Close() }()

		for _, table := range []string{"torrents", "torrent_trackers", "goose_db_version"} {
			var name string
			err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
			if err != nil {
				t.Errorf("table %s missing: %v", table, err)
			}
		}

		var version int64
		if err := s.db.QueryRow(`SELECT MAX(version_id) FROM goose_db_version`).Scan(&version); err != nil {
			t.Fatalf("read goose version: %v", err)
		}
		if version != 1 {
			t.Errorf("goose version = %d, want 1", version)
		}

		var idx string
		err = s.db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_torrent_trackers_tracker'`).Scan(&idx)
		if err != nil {
			t.Errorf("tracker index missing: %v", err)
		}
	})

	t.Run("existing database keeps its data", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "existing.db")

		first, err := OpenStore(path)
		if err != nil {
			t.Fatalf("OpenStore: %v", err)
		}
		if err := first.UpsertTorrent("hash1", "name1", 10, 20, 100); err != nil {
			t.Fatalf("UpsertTorrent: %v", err)
		}
		if err := first.SetTrackers("hash1", []string{"tracker.example.org"}, 100); err != nil {
			t.Fatalf("SetTrackers: %v", err)
		}
		if err := first.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		second, err := OpenStore(path)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		defer func() { _ = second.Close() }()

		name, up, down, _, _, _ := torrentRow(t, second, "hash1")
		if name != "name1" || up != 10 || down != 20 {
			t.Errorf("row = %q/%d/%d, want name1/10/20", name, up, down)
		}

		var applied int64
		if err := second.db.QueryRow(`SELECT COUNT(*) FROM goose_db_version WHERE version_id = 1`).Scan(&applied); err != nil {
			t.Fatalf("count goose rows: %v", err)
		}
		if applied != 1 {
			t.Errorf("migration 1 applied %d times, want exactly 1", applied)
		}

		stats, err := second.TrackerStats()
		if err != nil {
			t.Fatalf("TrackerStats: %v", err)
		}
		if len(stats) != 1 || stats[0].Tracker != "tracker.example.org" {
			t.Errorf("stats = %+v", stats)
		}
	})

	t.Run("unwritable path", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing-dir", "qbit.db")
		s, err := OpenStore(path)
		if err == nil {
			_ = s.Close()
			t.Fatal("OpenStore succeeded for a path under a missing directory")
		}
	})
}

func TestUpsertTorrent(t *testing.T) {
	t.Run("insert then accumulate deltas", func(t *testing.T) {
		s := newStore(t)

		if err := s.UpsertTorrent("h1", "first", 100, 200, 1000); err != nil {
			t.Fatalf("insert: %v", err)
		}
		name, up, down, lastUp, lastDown, updated := torrentRow(t, s, "h1")
		if name != "first" || up != 100 || down != 200 || lastUp != 100 || lastDown != 200 || updated != 1000 {
			t.Fatalf("after insert: %q %d %d %d %d %d", name, up, down, lastUp, lastDown, updated)
		}

		if err := s.UpsertTorrent("h1", "renamed", 150, 260, 1100); err != nil {
			t.Fatalf("update: %v", err)
		}
		name, up, down, lastUp, lastDown, updated = torrentRow(t, s, "h1")
		if name != "renamed" {
			t.Errorf("name = %q, want renamed", name)
		}
		if up != 150 || down != 260 {
			t.Errorf("totals = %d/%d, want 150/260", up, down)
		}
		if lastUp != 150 || lastDown != 260 || updated != 1100 {
			t.Errorf("last seen = %d/%d at %d", lastUp, lastDown, updated)
		}
	})

	t.Run("counter reset adds the raw value", func(t *testing.T) {
		s := newStore(t)

		if err := s.UpsertTorrent("h1", "n", 1000, 2000, 1); err != nil {
			t.Fatalf("insert: %v", err)
		}
		if err := s.UpsertTorrent("h1", "n", 30, 40, 2); err != nil {
			t.Fatalf("reset: %v", err)
		}

		_, up, down, lastUp, lastDown, _ := torrentRow(t, s, "h1")
		if up != 1030 || down != 2040 {
			t.Errorf("totals = %d/%d, want 1030/2040 after a counter reset", up, down)
		}
		if lastUp != 30 || lastDown != 40 {
			t.Errorf("last seen = %d/%d, want 30/40", lastUp, lastDown)
		}
	})

	t.Run("unchanged counters add nothing", func(t *testing.T) {
		s := newStore(t)

		if err := s.UpsertTorrent("h1", "n", 500, 600, 1); err != nil {
			t.Fatalf("insert: %v", err)
		}
		for i := range 3 {
			if err := s.UpsertTorrent("h1", "n", 500, 600, int64(2+i)); err != nil {
				t.Fatalf("repeat %d: %v", i, err)
			}
		}
		_, up, down, _, _, _ := torrentRow(t, s, "h1")
		if up != 500 || down != 600 {
			t.Errorf("totals = %d/%d, want 500/600", up, down)
		}
	})

	t.Run("does not disturb trackers_updated_at", func(t *testing.T) {
		s := newStore(t)

		if err := s.UpsertTorrent("h1", "n", 1, 1, 10); err != nil {
			t.Fatalf("insert: %v", err)
		}
		if err := s.SetTrackers("h1", []string{"a.example.org"}, 50); err != nil {
			t.Fatalf("SetTrackers: %v", err)
		}
		if err := s.UpsertTorrent("h1", "n", 2, 2, 60); err != nil {
			t.Fatalf("update: %v", err)
		}

		var updatedAt int64
		if err := s.db.QueryRow(`SELECT trackers_updated_at FROM torrents WHERE hash = ?`, "h1").Scan(&updatedAt); err != nil {
			t.Fatalf("select: %v", err)
		}
		if updatedAt != 50 {
			t.Errorf("trackers_updated_at = %d, want 50", updatedAt)
		}
	})
}

func TestTrackersStale(t *testing.T) {
	const base = int64(1700000000)

	s := newStore(t)

	t.Run("unknown torrent is stale", func(t *testing.T) {
		stale, err := s.TrackersStale("unknown", base, 3600)
		if err != nil {
			t.Fatalf("TrackersStale: %v", err)
		}
		if !stale {
			t.Error("stale = false, want true for a torrent with no row")
		}
	})

	if err := s.UpsertTorrent("h1", "n", 1, 1, base); err != nil {
		t.Fatalf("UpsertTorrent: %v", err)
	}

	t.Run("never fetched is stale", func(t *testing.T) {
		stale, err := s.TrackersStale("h1", base, 3600)
		if err != nil {
			t.Fatalf("TrackersStale: %v", err)
		}
		if !stale {
			t.Error("stale = false, want true when trackers_updated_at is 0")
		}
	})

	if err := s.SetTrackers("h1", []string{"a.example.org"}, base); err != nil {
		t.Fatalf("SetTrackers: %v", err)
	}

	tests := []struct {
		name   string
		now    int64
		maxAge int64
		want   bool
	}{
		{name: "just fetched", now: base, maxAge: 3600, want: false},
		{name: "inside the window", now: base + 3599, maxAge: 3600, want: false},
		{name: "exactly at the window", now: base + 3600, maxAge: 3600, want: true},
		{name: "past the window", now: base + 7200, maxAge: 3600, want: true},
		{name: "zero max age always stale", now: base, maxAge: 0, want: true},
		{name: "clock went backwards", now: base - 60, maxAge: 3600, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stale, err := s.TrackersStale("h1", tt.now, tt.maxAge)
			if err != nil {
				t.Fatalf("TrackersStale: %v", err)
			}
			if stale != tt.want {
				t.Errorf("stale = %v, want %v", stale, tt.want)
			}
		})
	}
}

func TestSetTrackers(t *testing.T) {
	trackersFor := func(t *testing.T, s *Store, hash string) []string {
		t.Helper()
		rows, err := s.db.Query(`SELECT tracker FROM torrent_trackers WHERE hash = ? ORDER BY tracker`, hash)
		if err != nil {
			t.Fatalf("query trackers: %v", err)
		}
		defer func() { _ = rows.Close() }()
		var out []string
		for rows.Next() {
			var tr string
			if err := rows.Scan(&tr); err != nil {
				t.Fatalf("scan: %v", err)
			}
			out = append(out, tr)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("rows: %v", err)
		}
		return out
	}

	equal := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}

	t.Run("replaces the previous set", func(t *testing.T) {
		s := newStore(t)
		if err := s.UpsertTorrent("h1", "n", 1, 1, 1); err != nil {
			t.Fatalf("UpsertTorrent: %v", err)
		}

		if err := s.SetTrackers("h1", []string{"a.example.org", "b.example.org"}, 10); err != nil {
			t.Fatalf("SetTrackers: %v", err)
		}
		if got := trackersFor(t, s, "h1"); !equal(got, []string{"a.example.org", "b.example.org"}) {
			t.Fatalf("trackers = %v", got)
		}

		if err := s.SetTrackers("h1", []string{"b.example.org", "c.example.org"}, 20); err != nil {
			t.Fatalf("SetTrackers: %v", err)
		}
		if got := trackersFor(t, s, "h1"); !equal(got, []string{"b.example.org", "c.example.org"}) {
			t.Errorf("trackers = %v, want the second set only", got)
		}

		var updatedAt int64
		if err := s.db.QueryRow(`SELECT trackers_updated_at FROM torrents WHERE hash = ?`, "h1").Scan(&updatedAt); err != nil {
			t.Fatalf("select: %v", err)
		}
		if updatedAt != 20 {
			t.Errorf("trackers_updated_at = %d, want 20", updatedAt)
		}
	})

	t.Run("duplicates collapse", func(t *testing.T) {
		s := newStore(t)
		if err := s.UpsertTorrent("h1", "n", 1, 1, 1); err != nil {
			t.Fatalf("UpsertTorrent: %v", err)
		}
		if err := s.SetTrackers("h1", []string{"a.example.org", "a.example.org"}, 10); err != nil {
			t.Fatalf("SetTrackers: %v", err)
		}
		if got := trackersFor(t, s, "h1"); !equal(got, []string{"a.example.org"}) {
			t.Errorf("trackers = %v, want one row", got)
		}
	})

	t.Run("empty list clears", func(t *testing.T) {
		s := newStore(t)
		if err := s.UpsertTorrent("h1", "n", 1, 1, 1); err != nil {
			t.Fatalf("UpsertTorrent: %v", err)
		}
		if err := s.SetTrackers("h1", []string{"a.example.org"}, 10); err != nil {
			t.Fatalf("SetTrackers: %v", err)
		}
		if err := s.SetTrackers("h1", nil, 20); err != nil {
			t.Fatalf("SetTrackers: %v", err)
		}
		if got := trackersFor(t, s, "h1"); len(got) != 0 {
			t.Errorf("trackers = %v, want none", got)
		}
	})

	t.Run("only touches the given hash", func(t *testing.T) {
		s := newStore(t)
		for _, h := range []string{"h1", "h2"} {
			if err := s.UpsertTorrent(h, "n", 1, 1, 1); err != nil {
				t.Fatalf("UpsertTorrent: %v", err)
			}
			if err := s.SetTrackers(h, []string{"shared.example.org"}, 10); err != nil {
				t.Fatalf("SetTrackers: %v", err)
			}
		}
		if err := s.SetTrackers("h1", nil, 20); err != nil {
			t.Fatalf("SetTrackers: %v", err)
		}
		if got := trackersFor(t, s, "h2"); !equal(got, []string{"shared.example.org"}) {
			t.Errorf("h2 trackers = %v, want them untouched", got)
		}
	})

	t.Run("unknown hash stores nothing in torrents", func(t *testing.T) {
		s := newStore(t)
		if err := s.SetTrackers("ghost", []string{"a.example.org"}, 10); err != nil {
			t.Fatalf("SetTrackers: %v", err)
		}
		if got := trackersFor(t, s, "ghost"); !equal(got, []string{"a.example.org"}) {
			t.Errorf("trackers = %v", got)
		}
	})
}

func TestTrackerStats(t *testing.T) {
	t.Run("aggregates per tracker and sorts by name", func(t *testing.T) {
		s := newStore(t)

		if err := s.UpsertTorrent("h1", "one", 100, 50, 1); err != nil {
			t.Fatalf("UpsertTorrent: %v", err)
		}
		if err := s.UpsertTorrent("h2", "two", 300, 150, 1); err != nil {
			t.Fatalf("UpsertTorrent: %v", err)
		}
		if err := s.SetTrackers("h1", []string{"zeta.example.org", "alpha.example.org"}, 1); err != nil {
			t.Fatalf("SetTrackers: %v", err)
		}
		if err := s.SetTrackers("h2", []string{"alpha.example.org"}, 1); err != nil {
			t.Fatalf("SetTrackers: %v", err)
		}

		stats, err := s.TrackerStats()
		if err != nil {
			t.Fatalf("TrackerStats: %v", err)
		}
		want := []TrackerStat{
			{Tracker: "alpha.example.org", Uploaded: 400, Downloaded: 200, Torrents: 2},
			{Tracker: "zeta.example.org", Uploaded: 100, Downloaded: 50, Torrents: 1},
		}
		if len(stats) != len(want) {
			t.Fatalf("stats = %+v, want %+v", stats, want)
		}
		for i := range want {
			if stats[i] != want[i] {
				t.Errorf("stats[%d] = %+v, want %+v", i, stats[i], want[i])
			}
		}
	})

	t.Run("totals survive a deleted torrent", func(t *testing.T) {
		s := newStore(t)

		if err := s.UpsertTorrent("gone", "removed", 999, 111, 1); err != nil {
			t.Fatalf("UpsertTorrent: %v", err)
		}
		if err := s.SetTrackers("gone", []string{"alpha.example.org"}, 1); err != nil {
			t.Fatalf("SetTrackers: %v", err)
		}
		if err := s.UpsertTorrent("live", "kept", 1, 1, 2); err != nil {
			t.Fatalf("UpsertTorrent: %v", err)
		}
		if err := s.SetTrackers("live", []string{"alpha.example.org"}, 2); err != nil {
			t.Fatalf("SetTrackers: %v", err)
		}

		stats, err := s.TrackerStats()
		if err != nil {
			t.Fatalf("TrackerStats: %v", err)
		}
		if len(stats) != 1 {
			t.Fatalf("stats = %+v, want one tracker", stats)
		}
		if stats[0].Uploaded != 1000 || stats[0].Downloaded != 112 || stats[0].Torrents != 2 {
			t.Errorf("stats[0] = %+v", stats[0])
		}
	})

	t.Run("tracker rows without a torrent row are skipped", func(t *testing.T) {
		s := newStore(t)
		if err := s.SetTrackers("ghost", []string{"alpha.example.org"}, 1); err != nil {
			t.Fatalf("SetTrackers: %v", err)
		}
		stats, err := s.TrackerStats()
		if err != nil {
			t.Fatalf("TrackerStats: %v", err)
		}
		if len(stats) != 0 {
			t.Errorf("stats = %+v, want none", stats)
		}
	})

	t.Run("empty store", func(t *testing.T) {
		s := newStore(t)
		stats, err := s.TrackerStats()
		if err != nil {
			t.Fatalf("TrackerStats: %v", err)
		}
		if len(stats) != 0 {
			t.Errorf("stats = %+v, want none", stats)
		}
	})

	t.Run("errors after close", func(t *testing.T) {
		s, err := OpenStore(filepath.Join(t.TempDir(), "closed.db"))
		if err != nil {
			t.Fatalf("OpenStore: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if _, err := s.TrackerStats(); err == nil {
			t.Error("TrackerStats succeeded on a closed store")
		}
		if err := s.UpsertTorrent("h1", "n", 1, 1, 1); err == nil {
			t.Error("UpsertTorrent succeeded on a closed store")
		}
		if _, err := s.TrackersStale("h1", 1, 1); err == nil {
			t.Error("TrackersStale succeeded on a closed store")
		}
		if err := s.SetTrackers("h1", nil, 1); err == nil {
			t.Error("SetTrackers succeeded on a closed store")
		}
	})
}
