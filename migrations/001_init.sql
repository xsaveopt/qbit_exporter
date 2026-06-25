-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS torrents (
    hash TEXT PRIMARY KEY,
    name TEXT NOT NULL DEFAULT '',
    uploaded INTEGER NOT NULL DEFAULT 0,
    downloaded INTEGER NOT NULL DEFAULT 0,
    last_uploaded INTEGER NOT NULL DEFAULT 0,
    last_downloaded INTEGER NOT NULL DEFAULT 0,
    trackers_updated_at INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL DEFAULT 0
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS torrent_trackers (
    hash TEXT NOT NULL,
    tracker TEXT NOT NULL,
    PRIMARY KEY (hash, tracker)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_torrent_trackers_tracker ON torrent_trackers(tracker);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS torrent_trackers;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS torrents;
-- +goose StatementEnd
