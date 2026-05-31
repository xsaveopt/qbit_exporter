# Running under systemd

Reference unit + service-user setup. Copy what you need; nothing here is mandatory — the binary runs fine in the foreground for testing.

## Install the binary

Download the binary for your architecture from the [latest release](https://github.com/sratabix/qbit_exporter/releases/latest) and drop it in `/usr/local/bin`:

```sh
sudo curl -fL -o /usr/local/bin/qbit_exporter \
  https://github.com/sratabix/qbit_exporter/releases/latest/download/qbit_exporter_linux_amd64
sudo chmod +x /usr/local/bin/qbit_exporter
```

Swap `amd64` for `arm64` on ARM hosts.

## Service user

A dedicated, no-shell, no-home system account:

```sh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin qbit_exporter
```

## Credentials

Keep the qBittorrent password out of the unit file. Put it in a root-only
environment file:

```sh
sudo install -m 0640 -o root -g qbit_exporter /dev/null /etc/qbit_exporter.env
sudoedit /etc/qbit_exporter.env
```

```ini
QBIT_URL=http://127.0.0.1:8080
QBIT_USERNAME=admin
QBIT_PASSWORD=changeme
# QBIT_PER_TORRENT=true   # enable per-torrent metrics on small instances
```

If qBittorrent has "Bypass authentication for clients on localhost" enabled,
leave `QBIT_USERNAME`/`QBIT_PASSWORD` unset — the exporter skips login.

## Unit file

Drop at `/etc/systemd/system/qbit_exporter.service`:

```ini
[Unit]
Description=qBittorrent Prometheus exporter
Documentation=https://github.com/sratabix/qbit_exporter
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=qbit_exporter
Group=qbit_exporter
EnvironmentFile=/etc/qbit_exporter.env
ExecStart=/usr/local/bin/qbit_exporter --web.listen-address=:9879
Restart=on-failure
RestartSec=5

# Hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
LockPersonality=true
MemoryDenyWriteExecute=true
RestrictRealtime=true
RestrictNamespaces=true
SystemCallFilter=@system-service
SystemCallErrorNumber=EPERM

[Install]
WantedBy=multi-user.target
```

Then:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now qbit_exporter
sudo journalctl -fu qbit_exporter
```

## Removing

```sh
sudo systemctl disable --now qbit_exporter
sudo rm /etc/systemd/system/qbit_exporter.service /etc/qbit_exporter.env
sudo rm /usr/local/bin/qbit_exporter
sudo userdel qbit_exporter
```
