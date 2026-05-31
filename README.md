# qbit_exporter

**Prometheus exporter for qBittorrent — global transfer, disk-cache and per-category stats scraped straight from the WebUI API, with opt-in per-torrent metrics.**

## Contents

- [How it works](#how-it-works)
- [Installation](#installation)
- [docker-compose](#docker-compose)
- [Configuration](#configuration)
- [Metrics](#metrics)
- [Prometheus & Grafana](#prometheus--grafana)
- [Image tags](#image-tags)
- [Environment variables](#environment-variables)

## How it works

On each Prometheus scrape the exporter logs in to the qBittorrent WebUI (caching the SID cookie, re-authenticating on expiry), then pulls `app/version`, `app/buildInfo` and a full `sync/maindata` dump. That single maindata call carries the global `server_state` (speeds, all-time totals, ratio, DHT nodes, disk-cache stats, free space) plus every torrent, which the exporter folds into per-state and per-category aggregates. Per-torrent series are emitted only when explicitly enabled, since a large instance can have thousands of torrents.

## Installation

**Binary** — grab the Linux binary for your arch from the [latest release](https://github.com/sratabix/qbit_exporter/releases/latest):

```sh
curl -fL -o qbit_exporter \
  https://github.com/sratabix/qbit_exporter/releases/latest/download/qbit_exporter_linux_amd64
chmod +x qbit_exporter
./qbit_exporter --qbit.url=http://localhost:8080 --qbit.username=admin --qbit.password=secret
```

To run it as a service, see [docs/systemd.md](docs/systemd.md).

**Docker**:

```sh
docker run -p 9879:9879 \
  -e QBIT_URL=http://host.docker.internal:8080 \
  -e QBIT_USERNAME=admin -e QBIT_PASSWORD=secret \
  ghcr.io/sratabix/qbit_exporter:latest
```

## docker-compose

```yaml
services:
  qbit_exporter:
    image: ghcr.io/sratabix/qbit_exporter:latest
    container_name: qbit_exporter
    restart: unless-stopped
    ports:
      - "9879:9879"
    environment:
      QBIT_URL: http://qbittorrent:8080
      QBIT_USERNAME: admin
      QBIT_PASSWORD: secret
```

```sh
docker compose up -d
```

## Configuration

Everything is set with flags or the equivalent environment variables (see the table below); flags win when both are present. Point it at your WebUI with `QBIT_URL` and give it credentials, or leave them blank if qBittorrent bypasses auth for localhost. Per-torrent metrics are off by default — enable them with `QBIT_PER_TORRENT=true` only on instances small enough that the extra cardinality is acceptable.

## Metrics

All metrics are prefixed `qbittorrent_`. Highlights:

| Metric                                                                                | Description                      |
| ------------------------------------------------------------------------------------- | -------------------------------- |
| `qbittorrent_up`                                                                      | 1 if the last scrape succeeded   |
| `qbittorrent_app_info{version,api_version,...}`                                       | Build info as labels, constant 1 |
| `qbittorrent_dl_speed_bytes` / `qbittorrent_up_speed_bytes`                           | Global transfer rates            |
| `qbittorrent_alltime_downloaded_bytes` / `qbittorrent_alltime_uploaded_bytes`         | All-time totals                  |
| `qbittorrent_global_ratio`                                                            | Global share ratio               |
| `qbittorrent_dht_nodes`, `qbittorrent_peer_connections`                               | Swarm connectivity               |
| `qbittorrent_free_space_on_disk_bytes`                                                | Free space on the save-path disk |
| `qbittorrent_read_cache_hits_ratio`, `qbittorrent_*_cache_overload_ratio`             | Disk-cache health                |
| `qbittorrent_torrents_state_count{state}`                                             | Torrent count per state          |
| `qbittorrent_torrents_category_count{category}`, `qbittorrent_category_*_speed_bytes` | Per-category aggregates          |
| `qbittorrent_torrent_*{hash,name,category,state}`                                     | Per-torrent series (opt-in)      |

`curl localhost:9879/metrics` for the full list with help text.

## Prometheus & Grafana

Scrape config:

```yaml
scrape_configs:
  - job_name: qbittorrent
    static_configs:
      - targets: ["localhost:9879"]
```

A ready-made dashboard lives at [docs/grafana-dashboard.json](docs/grafana-dashboard.json) — import it in Grafana and pick your Prometheus datasource.

## Image tags

`latest` for the latest stable release. `1`, `1.2`, `1.2.3` to pin to a major, minor, or patch line. Pre-releases like `1.2.3-rc1` are never tagged `latest`. `dev` tracks the tip of `main`, rebuilt on every commit. Images are published to `ghcr.io/sratabix/qbit_exporter` and built for `linux/amd64`.

## Environment variables

| Var                  | Default                 | Purpose                                                    |
| -------------------- | ----------------------- | ---------------------------------------------------------- |
| `QBIT_URL`           | `http://localhost:8080` | Base URL of the qBittorrent WebUI.                         |
| `QBIT_USERNAME`      | _(empty)_               | WebUI username. Leave blank if localhost auth is bypassed. |
| `QBIT_PASSWORD`      | _(empty)_               | WebUI password.                                            |
| `QBIT_EXPORTER_ADDR` | `:9879`                 | HTTP listen address.                                       |
| `QBIT_EXPORTER_PATH` | `/metrics`              | Path to expose metrics on.                                 |
| `QBIT_PER_TORRENT`   | `false`                 | Emit per-torrent metrics (raises cardinality).             |
| `QBIT_TIMEOUT`       | `10s`                   | Per-scrape timeout.                                        |
| `QBIT_TLS_INSECURE`  | `false`                 | Skip TLS verification for self-signed HTTPS WebUIs.        |
