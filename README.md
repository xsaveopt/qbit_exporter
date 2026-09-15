# qbit_exporter

A Prometheus exporter for qBittorrent that reads the WebUI api on every scrape and turns it into global transfer, disk cache, per-state and per-category metrics, with per-torrent series available as an opt-in.
Alongside those it keeps a small SQLite database of what each torrent has uploaded and downloaded, which lets the share ratio per tracker keep counting torrents after they have been deleted.

## Running

Linux binaries for amd64 and arm64 are attached to every [release](https://github.com/xsaveopt/qbit_exporter/releases/latest).

```sh
curl -fL -o qbit_exporter https://github.com/xsaveopt/qbit_exporter/releases/latest/download/qbit_exporter_linux_amd64
chmod +x qbit_exporter
QBIT_URL={url} QBIT_USERNAME={username} QBIT_PASSWORD={password} ./qbit_exporter
```

A unit file and service user setup for running it under systemd are in docs/systemd.md.

The container image is ghcr.io/xsaveopt/qbit_exporter, built for linux/amd64, and docker-compose.yml in the repo is a working starting point.

```sh
docker run -p 9879:9879 -v qbit_exporter_data:/data \
  -e QBIT_URL={url} -e QBIT_USERNAME={username} -e QBIT_PASSWORD={password} \
  ghcr.io/xsaveopt/qbit_exporter:latest
```

Inside the image the database lives under /data, which is owned by the nonroot user the exporter runs as, so a volume mounted there keeps tracker history across restarts.
The latest tag follows the newest stable release, while 1, 1.2 and 1.2.3 pin a major, minor or patch line and dev is rebuilt from every commit to main.
Pre-releases such as 1.2.3-rc1 only get their own version tag.

Once it is up, point a Prometheus scrape job at port 9879 and import docs/grafana-dashboard.json into Grafana for a ready-made dashboard.
The server also answers on /healthz with a plain ok.

## Configuration

Every setting below can also be passed as a command line flag, and the flag wins when both are set.
Leave the username and password empty when qBittorrent bypasses authentication for clients on localhost.

Per-torrent metrics add a series for every torrent on each of their metrics, which on a large instance runs into the thousands.

Per-tracker stats need a writable path for the database, and if it cannot be opened the exporter logs an error and keeps running with those stats turned off.
Trackers are grouped by hostname, and a torrent that announces to several of them counts in full toward each one.
The ratio for a tracker is the summed upload over the summed download of every torrent it has ever seen, and each torrent's tracker list is fetched again once the refresh interval has passed.

| Variable             | Default                 | Purpose                                                            |
| -------------------- | ----------------------- | ------------------------------------------------------------------ |
| QBIT_URL             | `http://localhost:8080` | Base URL of the qBittorrent WebUI.                                 |
| QBIT_USERNAME        | empty                   | WebUI username.                                                    |
| QBIT_PASSWORD        | empty                   | WebUI password.                                                    |
| QBIT_EXPORTER_ADDR   | `:9879`                 | HTTP listen address.                                               |
| QBIT_EXPORTER_PATH   | `/metrics`              | Path the metrics are served on.                                    |
| QBIT_PER_TORRENT     | `false`                 | Export per-torrent metrics.                                        |
| QBIT_TRACKER_STATS   | `true`                  | Track per-tracker totals and ratio in SQLite.                      |
| QBIT_DB_PATH         | `qbit_exporter.db`      | SQLite database path, set to `/data/qbit_exporter.db` in the image. |
| QBIT_TRACKER_REFRESH | `1h`                    | How often each torrent's tracker list is fetched again.            |
| QBIT_TIMEOUT         | `10s`                   | Per-scrape timeout.                                                |
| QBIT_TLS_INSECURE    | `false`                 | Skip TLS certificate verification for self-signed HTTPS WebUIs.    |

## Metrics

Every metric name starts with qbittorrent_, and the metrics endpoint lists all of them with their help text.
These are the ones most dashboards are built on:

| Metric                                                                   | Description                                        |
| ------------------------------------------------------------------------ | -------------------------------------------------- |
| `qbittorrent_up`                                                         | 1 if the last scrape succeeded                     |
| `qbittorrent_app_info{version,api_version,...}`                          | Build info as labels, constant 1                   |
| `qbittorrent_dl_speed_bytes`, `qbittorrent_up_speed_bytes`               | Global transfer rates                              |
| `qbittorrent_alltime_downloaded_bytes`, `qbittorrent_alltime_uploaded_bytes` | All-time totals                                |
| `qbittorrent_global_ratio`                                               | Global share ratio                                 |
| `qbittorrent_dht_nodes`, `qbittorrent_peer_connections`                  | Swarm connectivity                                 |
| `qbittorrent_free_space_on_disk_bytes`                                   | Free space on the default save path disk           |
| `qbittorrent_read_cache_hits_ratio`, `qbittorrent_*_cache_overload_ratio` | Disk cache health                                 |
| `qbittorrent_torrents_state_count{state}`                                | Torrent count per state                            |
| `qbittorrent_torrents_category_count{category}`, `qbittorrent_category_*` | Per-category counts, speeds and size              |
| `qbittorrent_torrent_*{hash,name,category,state}`                        | Per-torrent series, opt-in                         |
| `qbittorrent_tracker_ratio{tracker}`                                     | Share ratio per tracker                            |
| `qbittorrent_tracker_uploaded_bytes{tracker}`, `qbittorrent_tracker_downloaded_bytes{tracker}` | Lifetime totals per tracker, including deleted torrents |
| `qbittorrent_tracker_torrents_count{tracker}`                            | Torrents ever seen announcing to a tracker         |

## License

GPL-2.0, see LICENSE.
