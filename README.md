# vexil

vexil is a simple, self-hosted uptime monitor. One binary, one data folder, no external database.

- Monitors HTTP, TCP, ping, DNS and push targets.
- Sends alerts to email, Slack, Discord, Telegram, ntfy and webhooks.
- Serves a public status page and status badges.
- Runs as one static Go binary with SQLite inside the data folder.

## Screenshots

The dashboard in the light theme:

![Dashboard](docs/screenshots/dashboard.png)

A monitor in the dark theme, with the response chart, uptime and incidents:

![Monitor detail](docs/screenshots/monitor.png)

The public status page with a custom name and logo:

![Public status page](docs/screenshots/status.png)

The dashboard on a phone:

<img src="docs/screenshots/dashboard-mobile.png" width="300" alt="Dashboard on a phone">

## Quick start

Download a binary from the [releases page](https://github.com/InAtTheGeekEnd/vexil/releases) and run it:

```
./vexil
```

Or build it from source:

```
go run ./cmd/vexil
```

Open http://localhost:8080. On the first visit vexil asks you to set the admin password.

### Docker

```
docker run -d --name vexil -p 8080:8080 -v vexil-data:/data \
  --sysctl net.ipv4.ping_group_range="0 2147483647" \
  ghcr.io/inatthegeekend/vexil:latest
```

The image is distroless and runs as the `nonroot` user (uid 65532). The `/data` folder in the image belongs to that user. A named volume takes over that ownership. For a host folder, run `chown 65532:65532 ./data` first. The sysctl is only for ping monitors.

`docker compose`:

```yaml
services:
  vexil:
    image: ghcr.io/inatthegeekend/vexil:latest
    ports:
      - "8080:8080"
    volumes:
      - vexil-data:/data
    environment:
      VEXIL_BASE_URL: https://status.example.com
    sysctls:
      net.ipv4.ping_group_range: "0 2147483647"
    restart: unless-stopped

volumes:
  vexil-data:
```

The image has a `HEALTHCHECK` that runs `vexil healthcheck`. It calls `/readyz` and exits with 0 or 1.

### systemd

```ini
[Unit]
Description=vexil uptime monitor
After=network-online.target
Wants=network-online.target

[Service]
User=vexil
Group=vexil
ExecStart=/usr/local/bin/vexil
Environment=VEXIL_ADDR=127.0.0.1:8080
Environment=VEXIL_DATA=/var/lib/vexil
Environment=VEXIL_BASE_URL=https://status.example.com
StateDirectory=vexil
Restart=on-failure
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true

[Install]
WantedBy=multi-user.target
```

### Kubernetes

vexil is one pod with one volume. Use the health endpoints as probes:

```yaml
livenessProbe:
  httpGet:
    path: /healthz
    port: 8080
readinessProbe:
  httpGet:
    path: /readyz
    port: 8080
```

### Settings

vexil has three environment variables. Everything else is set in the UI.

| Variable | Default | Purpose |
|---|---|---|
| `VEXIL_ADDR` | `:8080` | The listen address. |
| `VEXIL_DATA` | `./data` | The folder for the SQLite file and uploads. |
| `VEXIL_BASE_URL` | empty | The public URL. vexil uses it in notification links. |

### Ping monitors

vexil uses unprivileged ICMP (UDP) ping. On Linux the kernel must allow it for the group that runs vexil:

```
sysctl -w net.ipv4.ping_group_range="0 2147483647"
```

In Docker add `--sysctl net.ipv4.ping_group_range="0 2147483647"` to `docker run`. For systemd put the sysctl in `/etc/sysctl.d/`.

### Use HTTPS for real installs

Every open vexil tab keeps one live connection to the server for updates. Over plain HTTP a browser allows only 6 connections to one server, so several open tabs can block each other. Put vexil behind a reverse proxy with HTTPS. HTTP/2 lifts the limit.

### Public status page and custom domain

The status page is at `/status`. It shows the monitors that have **Show on status page** on. Every public monitor also has a badge at `/badge/{id}.svg`; the monitor page shows the URL.

To serve the status page on its own domain, point the domain at vexil through a reverse proxy.

Caddy:

```
status.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

nginx:

```
server {
    listen 443 ssl http2;
    server_name status.example.com;
    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto https;
    }
}
```

Set `VEXIL_BASE_URL=https://status.example.com` so links in alerts use the domain. Settings has the brand: the name, the logo, the accent color and the "Powered by" line.

### Backup

Copy the data folder. It holds everything. Raw check results are kept for 30 days. A daily summary per monitor is kept forever.

### Forgot your password?

Run this on the server. It works while vexil runs and logs out every browser.

```
vexil reset-password
```

## Development

```
go build ./cmd/vexil        # build
go test -race ./...         # test
go vet ./...                # vet
staticcheck ./...           # lint
gofmt -l .                  # must print nothing
```

The build works with `CGO_ENABLED=0`. See `SPEC.md` for the product specification.

CI runs `gofmt`, `go vet`, `staticcheck`, `go test -race` and a Docker build on every push. A tag like `v1.2.3` builds the release binaries with GoReleaser and pushes the image to `ghcr.io/inatthegeekend/vexil`.

## License

The source code is released under the MIT license. See [LICENSE](LICENSE).

### Name and logo

The vexil name and the logo files in `web/static/brand/` are not covered by the MIT license. All rights reserved. See [web/static/brand/LICENSE](web/static/brand/LICENSE). You can replace the name and logo in the UI under Settings.

The Inter font in `web/static/fonts/` is licensed under the SIL Open Font License. See [web/static/fonts/OFL.txt](web/static/fonts/OFL.txt). The file is a Latin subset of Inter Variable, made with `pyftsubset` from fonttools. Inter declares no Reserved Font Name, so the subset keeps the name.
