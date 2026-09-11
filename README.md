# vexil

vexil is a simple, self-hosted uptime monitor. One binary, one data folder, no external database.

- Monitors HTTP, TCP, ping, DNS and push targets.
- Sends alerts to email, Slack, Discord, Telegram, ntfy and webhooks.
- Serves a public status page and status badges.
- Runs as one static Go binary with SQLite inside the data folder.

## Quick start

```
go run ./cmd/vexil
```

Open http://localhost:8080. On the first visit vexil asks you to set the admin password.

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

### Backup

Copy the data folder. It holds everything.

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

## License

The source code is released under the MIT license. See [LICENSE](LICENSE).

### Name and logo

The vexil name and the logo files in `web/static/brand/` are not covered by the MIT license. All rights reserved. See [web/static/brand/LICENSE](web/static/brand/LICENSE). You can replace the name and logo in the UI under Settings.

The Inter font in `web/static/fonts/` is licensed under the SIL Open Font License. See [web/static/fonts/OFL.txt](web/static/fonts/OFL.txt).
