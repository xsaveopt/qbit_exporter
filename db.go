package main

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log/slog"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const migrationsDir = "migrations"

type Store struct {
	db *sql.DB
}

type TrackerStat struct {
	Tracker    string
	Uploaded   int64
	Downloaded int64
	Torrents   int64
}

func OpenStore(path string) (*Store, error) {
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) migrate() error {
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(gooseLogger{})
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("goose dialect: %w", err)
	}
	if err := goose.Up(s.db, migrationsDir); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) UpsertTorrent(hash, name string, rawUp, rawDown, now int64) error {
	_, err := s.db.Exec(`
INSERT INTO torrents (hash, name, uploaded, downloaded, last_uploaded, last_downloaded, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(hash) DO UPDATE SET
	name = excluded.name,
	uploaded = uploaded + CASE
		WHEN excluded.last_uploaded >= last_uploaded THEN excluded.last_uploaded - last_uploaded
		ELSE excluded.last_uploaded END,
	downloaded = downloaded + CASE
		WHEN excluded.last_downloaded >= last_downloaded THEN excluded.last_downloaded - last_downloaded
		ELSE excluded.last_downloaded END,
	last_uploaded = excluded.last_uploaded,
	last_downloaded = excluded.last_downloaded,
	updated_at = excluded.updated_at`,
		hash, name, rawUp, rawDown, rawUp, rawDown, now)
	return err
}

func (s *Store) TrackersStale(hash string, now, maxAgeSec int64) (bool, error) {
	var updated int64
	err := s.db.QueryRow(`SELECT trackers_updated_at FROM torrents WHERE hash = ?`, hash).Scan(&updated)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return now-updated >= maxAgeSec, nil
}

func (s *Store) SetTrackers(hash string, trackers []string, now int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM torrent_trackers WHERE hash = ?`, hash); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO torrent_trackers (hash, tracker) VALUES (?, ?)`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, tr := range trackers {
		if _, err := stmt.Exec(hash, tr); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE torrents SET trackers_updated_at = ? WHERE hash = ?`, now, hash); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) TrackerStats() ([]TrackerStat, error) {
	rows, err := s.db.Query(`
SELECT tt.tracker, COALESCE(SUM(t.uploaded), 0), COALESCE(SUM(t.downloaded), 0), COUNT(*)
FROM torrent_trackers tt
JOIN torrents t ON t.hash = tt.hash
GROUP BY tt.tracker
ORDER BY tt.tracker`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []TrackerStat
	for rows.Next() {
		var st TrackerStat
		if err := rows.Scan(&st.Tracker, &st.Uploaded, &st.Downloaded, &st.Torrents); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

type gooseLogger struct{}

func (gooseLogger) Fatalf(format string, v ...any) {
	slog.Error("goose: " + fmt.Sprintf(format, v...))
}

func (gooseLogger) Printf(format string, v ...any) {
	slog.Debug("goose: " + fmt.Sprintf(format, v...))
}

var _ goose.Logger = gooseLogger{}
