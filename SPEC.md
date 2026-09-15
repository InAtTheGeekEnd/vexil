# vexil: Product Specification

Product name: **vexil**. Always lowercase, also at the start of a sentence.
Repository: https://github.com/InAtTheGeekEnd/vexil
Go module: `github.com/InAtTheGeekEnd/vexil`

vexil is the default brand. A white-label user can replace it in the UI (see White label). The code must not hard-code the name outside the brand defaults.

Version: 1.0 draft
License: MIT

---

## 1. Goals

1. **Simplicity.** This goal wins every conflict.
   - One binary. One data folder. No external database. No Node build step.
   - A new user adds the first monitor in less than 60 seconds.
   - Sensible defaults replace settings. If a default works for 90% of users, do not add a setting.
2. **Beauty.** The UI must look calm, precise and expensive. Every screen gets design attention, including empty states, errors and the login page.

### Rule for scope decisions

When somebody proposes a feature, ask: "Can a user get this result without the feature?" If yes, do not build it.

---

## 2. Non-goals (v1)

vexil does not do these things. Do not build them.

- More than one user, teams, or roles.
- Per-monitor notification routing. All channels get all alerts.
- Maintenance windows.
- More than one status page.
- Postgres, MySQL, Redis, or any external service.
- Clusters, multi-region checks, or remote agents.
- Plugins or scripting.
- A full CRUD API.
- Custom HTTP methods, headers, or request bodies.
- Translations (English only in v1).
- Charts that the user can configure.

---

## 3. Technical stack

| Part | Choice | Reason |
|---|---|---|
| Language | Go, latest stable (1.25 or later) | One static binary. |
| HTTP router | `net/http` standard library (method and path patterns) | No framework. |
| Database | SQLite through `modernc.org/sqlite` | Pure Go. No CGO. Cross-compiles easily. |
| Templates | `html/template` | Server-rendered pages. Fast. No build step. |
| Interactivity | htmx (vendored) plus small vanilla JS | No npm. No bundler. |
| Live updates | Server-Sent Events (SSE) | One-way push is enough. Simpler than WebSockets. |
| Charts | Server-rendered SVG | No chart library. Full control of the look. |
| Ping | `github.com/prometheus-community/pro-bing` | Standard Go ICMP library. |
| Passwords | `golang.org/x/crypto/bcrypt` | Standard. |
| Email | `net/smtp` standard library | No dependency. |
| Assets | `embed` package | Templates, CSS, JS and fonts live inside the binary. |

Keep the dependency list at five direct modules or fewer. Any new dependency needs a written reason in the pull request.

---

## 4. Configuration

### 4.1 Environment variables

These are the only settings outside the UI. The variable names use uppercase, as is normal for environment variables.

| Variable | Default | Purpose |
|---|---|---|
| `VEXIL_ADDR` | `:8080` | The listen address. |
| `VEXIL_DATA` | `./data` | The folder for the SQLite file and uploads. |
| `VEXIL_BASE_URL` | empty | The public URL. vexil uses it in notification links. |

### 4.2 First run

1. The user opens the web UI.
2. vexil shows the setup screen.
3. The user enters one admin password (twice).
4. vexil shows the empty dashboard with one button: "Add your first monitor".

There is no username. There is no email for the admin.

### 4.3 Password reset

The admin has no email address, so there is no reset email. The user resets the password on the server:

```
vexil reset-password
```

Docker:

```
docker exec -it vexil vexil reset-password
```

- The command asks for the new password twice. It does not accept the password as an argument, so the password does not go into the shell history.
- The command writes the new bcrypt hash to the database.
- The command deletes all sessions. All browsers must log in again.
- The command works while the server runs.
- The login page shows one line of help: "Forgot your password? Run `vexil reset-password` on the server."

Only a person with access to the server can reset the password. This is the security model.

### 4.4 Fixed behavior (not configurable)

| Behavior | Value |
|---|---|
| Check timeout | 10 seconds |
| Failures before DOWN | 2 checks in a row |
| Next check after a failed check | 30 seconds, not the full interval |
| Successes before UP | 1 check |
| HTTP redirects | Follow, maximum 10 |
| Keyword search limit | First 1 MB of the body |
| TLS certificate warning | 14 days before expiry, one alert |
| Raw check retention | 30 days |
| Hourly summary retention | 30 days |
| Daily summary retention | Forever |
| Concurrent checks | 50 at the same time |

---

## 5. Monitor types

Five types. Each type has a small number of fields.

All monitors have: **Name**, **Interval** (30s, 1m, 5m, 15m, 30m, 1h, 6h, 12h, 24h; default 1m), **Show on status page** (on/off, default off).

### 5.1 HTTP(S)

- Field: **URL**.
- Optional field: **Keyword**. The check fails if the body does not contain the keyword (case-sensitive).
- Method: GET.
- Success: the final response has status 200 to 299.
- For HTTPS: vexil records the certificate expiry date and shows it on the detail page. vexil sends one warning alert 14 days before expiry.
- An invalid certificate is a failure. There is no "ignore TLS errors" option.

### 5.2 TCP port

- Fields: **Host**, **Port**.
- Success: the TCP connection opens.

### 5.3 Ping

- Field: **Host**.
- Sends 3 ICMP echo packets. Success: at least 1 reply.
- Latency: the average of the replies.
- Uses unprivileged (UDP) ping on Linux. Document the `net.ipv4.ping_group_range` sysctl for Docker and systemd.

### 5.4 DNS

- Field: **Hostname**.
- Optional field: **Expected IP**.
- Success: the name resolves to at least one A or AAAA record. If the user gives an expected IP, one record must match it.
- Uses the system resolver.

### 5.5 Push (heartbeat)

- No fields. vexil generates a secret URL: `{BASE_URL}/push/{token}`.
- An external job (cron, backup script) calls the URL with GET or POST.
- The monitor goes DOWN when no push arrives within the interval plus 25%, with a minimum of 30 seconds extra.
- The detail page shows the URL and a copy button, plus a one-line `curl` example.

---

## 6. Check engine

### 6.1 States

| State | Meaning |
|---|---|
| `PENDING` | New monitor. No result yet. |
| `UP` | The last check succeeded. |
| `DOWN` | 2 or more checks failed in a row. |
| `PAUSED` | The user paused the monitor. No checks run. |

Uptime is the time outside incidents, not a count of checks. A day's uptime is the seconds of the day not inside an incident, divided by the seconds the monitor was live that day. A monitor is live from its creation, except while it is paused: a paused period is not live. A day with no incident is 100%. A failed check that opened no incident counts nowhere. The 24 h, 7 d, 30 d and 90 d figures use the same rule over their windows, which end now. The `pauses` table records the paused periods. No table counts checks: `hourly.ok` is the weight of the average latency, and no uptime figure reads it. One function computes every uptime figure, and the dashboard, the monitor detail page and the public status page all call it.

### 6.2 Transitions

- `PENDING` to `UP`: first success. No alert.
- `PENDING` or `UP` to `DOWN`: second failure in a row. Send DOWN alert. Open an incident.
- `DOWN` to `UP`: first success. Send UP alert. Close the incident.
- Any state to `PAUSED`: user action. No alert.

vexil sends each alert once. There are no repeat reminders.

### 6.3 Design

- One goroutine per monitor with its own ticker.
- Add a random start offset so checks do not all run at the same second. The offset is at most 60 seconds, or the interval if that is shorter.
- After a restart, schedule the next check from the last check time in the database, not from the start time. A push monitor keeps its last push time.
- A semaphore limits concurrent checks to 50.
- Checkers send results into one channel. One writer goroutine writes to SQLite. This avoids lock contention.
- The writer publishes each result to an in-memory event hub. The SSE handler reads from the hub.
- Changes to a monitor (edit, pause, delete) restart only that monitor's goroutine.
- Shutdown: on SIGINT or SIGTERM, finish within 10 seconds. One budget of 8 seconds covers the stages in order: stop tickers and wait for running checks, flush the writer, wait for the alert calls, then wait for the deliveries in progress. Each stage gets what is left of the budget. Then close the database.

### 6.4 Checker interface

```go
type Result struct {
    OK         bool
    Latency    time.Duration
    StatusCode int       // HTTP only
    Error      string    // short, human-readable: "HTTP 503", "connection refused", "keyword not found"
    CertExpiry time.Time // HTTPS only
}

type Checker interface {
    Check(ctx context.Context) Result
}
```

Error strings must be short and readable by non-experts. They appear in alerts and in the UI.

---

## 7. Notifications

### 7.1 Channels

Seven channel types. All enabled channels get all alerts.

| Channel | Fields |
|---|---|
| Email (SMTP) | Host, port, username, password, from, to |
| Slack | Incoming webhook URL |
| Discord | Webhook URL |
| Telegram | Bot token, chat ID |
| ntfy | Topic URL, optional access token |
| Pushover | User key, application token, optional "Repeat until acknowledged" switch with a retry interval (1 to 60 minutes, default 1) and an expiry time (1 to 180 minutes, default 60) |
| Webhook | URL. vexil POSTs JSON. |

Each channel has a **Send test** button and an on/off switch.

### 7.2 Messages

DOWN:
```
🔴 API is down
Reason: HTTP 503
Since: 14:02 UTC
https://status.example.com/monitors/12
```

UP:
```
🟢 API is up again
Down for: 4m 12s
https://status.example.com/monitors/12
```

Certificate warning:
```
🟡 The TLS certificate for api.example.com expires in 14 days (3 Oct 2026)
```

Slack and Discord use their rich formats (color bar in the status color). Email uses a simple branded HTML template plus a plain-text part.

Pushover sends every message with normal priority (0). When "Repeat until acknowledged" is on, DOWN alerts use emergency priority (2): Pushover repeats the alert at the retry interval until the user acknowledges it or the expiry time ends. UP alerts, certificate warnings and test messages always use normal priority, never emergency.

When the monitor comes back UP, vexil cancels the repeats of its DOWN alert. vexil stores no Pushover receipt. Instead, the emergency DOWN alert carries one Pushover tag (the `tags` parameter) in the form `m<monitor ID>-<incident start in unix seconds>`, for example `m12-1757599320`. On the UP transition, each Pushover channel with the switch on calls `POST /1/receipts/cancel_by_tag/<tag>.json` with its application token. If the switch is off at that time, vexil does not cancel, and the alert repeats until its expiry time. A failed cancel gets the retries from 7.4 and is then logged. It does not hold back the UP alert. If the incident of a DOWN alert is closed by the time that alert is delivered, vexil cancels the alert right after the delivery, with the alert's own tag. This holds even when the monitor is DOWN again in a new incident.

### 7.3 Webhook payload

```json
{
  "event": "down",
  "monitor": { "id": 12, "name": "API", "type": "http", "target": "https://api.example.com" },
  "reason": "HTTP 503",
  "at": "2026-09-11T14:02:00Z",
  "url": "https://status.example.com/monitors/12"
}
```

`event` is one of: `down`, `up`, `cert_expiring`.

### 7.4 Delivery

- Send in a separate goroutine. A slow channel must not block checks.
- Retry 3 times with backoff (5s, 30s, 2m). Then log the failure and show it on the Notifications page.
- Retries live in memory. A restart can lose a pending retry.

---

## 8. Data model

```sql
CREATE TABLE monitors (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,  -- never reused: ids are in badge URLs, alert links and webhook payloads
  name        TEXT NOT NULL,
  type        TEXT NOT NULL,          -- http, tcp, ping, dns, push
  target      TEXT NOT NULL,          -- URL, host, host:port, or hostname
  keyword     TEXT,
  expected_ip TEXT,
  push_token  TEXT UNIQUE,
  interval_s  INTEGER NOT NULL DEFAULT 60,
  public      INTEGER NOT NULL DEFAULT 0,
  paused      INTEGER NOT NULL DEFAULT 0,
  position    INTEGER NOT NULL DEFAULT 0,  -- drag order inside the group, from 1
  created_at  INTEGER NOT NULL,
  cert_warned_at INTEGER,             -- expiry of the certificate the expiry warning was sent for
  group_id    INTEGER                 -- monitor_groups.id; NULL means in no group
);

CREATE TABLE monitor_groups (         -- not "groups": GROUPS is an SQLite keyword
  id          INTEGER PRIMARY KEY,
  name        TEXT NOT NULL,          -- 1 to 60 characters
  position    INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL
);
CREATE UNIQUE INDEX monitor_groups_name ON monitor_groups(name COLLATE NOCASE);

CREATE TABLE checks (
  monitor_id  INTEGER NOT NULL,
  at          INTEGER NOT NULL,       -- unix seconds
  ok          INTEGER NOT NULL,
  latency_ms  INTEGER,
  status_code INTEGER,
  error       TEXT,
  FOREIGN KEY (monitor_id) REFERENCES monitors(id) ON DELETE CASCADE
);
CREATE INDEX checks_monitor_at ON checks(monitor_id, at, ok, latency_ms);  -- covering: stats read ok and latency from the index

CREATE TABLE daily (
  monitor_id  INTEGER NOT NULL,
  day         TEXT NOT NULL,          -- YYYY-MM-DD, UTC
  avg_latency INTEGER,                -- of the successful checks, weighted by the ok of each hour
  PRIMARY KEY (monitor_id, day),
  FOREIGN KEY (monitor_id) REFERENCES monitors(id) ON DELETE CASCADE
);

CREATE TABLE hourly (
  monitor_id  INTEGER NOT NULL,
  hour        INTEGER NOT NULL,       -- unix seconds, on the hour, UTC
  ok          INTEGER NOT NULL,       -- successful checks: the weight of avg_latency, not an uptime figure
  avg_latency INTEGER,                -- of the successful checks
  PRIMARY KEY (monitor_id, hour),
  FOREIGN KEY (monitor_id) REFERENCES monitors(id) ON DELETE CASCADE
);

CREATE TABLE incidents (
  id          INTEGER PRIMARY KEY,
  monitor_id  INTEGER NOT NULL,
  started_at  INTEGER NOT NULL,
  ended_at    INTEGER,
  reason      TEXT,
  FOREIGN KEY (monitor_id) REFERENCES monitors(id) ON DELETE CASCADE
);

CREATE TABLE pauses (                 -- the periods a monitor was paused: not live for uptime (section 6.1)
  monitor_id  INTEGER NOT NULL,
  started_at  INTEGER NOT NULL,
  ended_at    INTEGER,                -- NULL while the pause lasts
  FOREIGN KEY (monitor_id) REFERENCES monitors(id) ON DELETE CASCADE
);

CREATE TABLE channels (
  id          INTEGER PRIMARY KEY,
  type        TEXT NOT NULL,
  name        TEXT NOT NULL,
  config      TEXT NOT NULL,          -- JSON
  enabled     INTEGER NOT NULL DEFAULT 1,
  last_error  TEXT,                   -- the last failed delivery; NULL when the last one worked
  last_error_at INTEGER               -- unix seconds of that failure
);

CREATE TABLE sessions (
  token_hash  TEXT PRIMARY KEY,       -- SHA-256 of the cookie token
  created_at  INTEGER NOT NULL,
  expires_at  INTEGER NOT NULL
);

CREATE TABLE settings (
  key         TEXT PRIMARY KEY,
  value       TEXT NOT NULL
);
```

- Use WAL mode.
- Open every connection with `PRAGMA foreign_keys = ON`, so a deleted monitor takes its checks, hourly rows, daily rows, incidents and pauses with it.
- Migrations: numbered `.sql` files in `internal/store/migrations`, embedded, run at startup.
- A background job runs 30 seconds after every full hour. It writes the `hourly` rows of the hours that have finished and fills any missing hour of the last 30 days. It builds `daily` from `hourly`, not from `checks`. It deletes `checks` and `hourly` rows older than 30 days.
- The 24-hour sparkline and the 7-day and 30-day response charts read `hourly`. An hour without a row yet, as the current hour, comes from `checks`.
- Backup: the user runs `vexil backup <path>` and copies that file. Copying the live data folder can corrupt the copy, because the database runs in WAL mode.

---

## 9. Web UI

### 9.1 Pages

| Path | Page | Access |
|---|---|---|
| `/setup` | Set admin password | First run only |
| `/login` | Login | Public |
| `/` | Dashboard | Admin |
| `/monitors/new` | Add monitor | Admin |
| `/monitors/{id}` | Monitor detail | Admin |
| `/monitors/{id}/edit` | Edit monitor | Admin |
| `/groups` | Monitor groups | Admin |
| `/notifications` | Channels | Admin |
| `/settings` | Brand and password | Admin |
| `/status` | Public status page | Public |
| `/push/{token}` | Push endpoint | Public |
| `/healthz` | Liveness check for vexil itself | Public |
| `/readyz` | Readiness check for vexil itself | Public |
| `/events` | SSE stream | Admin |
| `/badge/{id}.svg` | Status badge (public monitors only) | Public |

### 9.2 Health endpoints

These endpoints report the health of vexil itself. Load balancers, Docker and Kubernetes use them.

| Endpoint | Checks | Success | Failure |
|---|---|---|---|
| `GET /healthz` | The process serves HTTP. No other checks. | `200`, body `ok` | No response |
| `GET /readyz` | The database answers a ping. Migrations are complete. The check engine runs. | `200`, JSON | `503`, JSON |

`/readyz` body:

```json
{ "status": "ok", "database": "ok", "engine": "ok" }
```

On failure, the failed part shows a short reason, for example `"database": "locked"`.

- Both endpoints need no login.
- Both endpoints respond in less than 100 ms.
- Both endpoints do not write to the access log, so they do not flood it.
- Both endpoints do not show version numbers or internal details.

### 9.3 Dashboard

- Header: overall status sentence. Examples: "All systems operational", "2 monitors are down".
- One row per monitor. Each row shows:
  - Status dot.
  - Name. Target below it in muted text.
  - 30-day uptime bar (30 segments, one per day).
  - Uptime percentage for 30 days, by the rule of section 6.1 over the last 30 days.
  - Response time sparkline (last 24 hours).
  - "Checked 12s ago" in muted text.
- The whole row is a link to the detail page.
- Groups show in position order. Each group is a heading with its monitors below it. A group with no monitors still shows its heading, so the user can drag monitors into it.
- Monitors in no group go below all the groups, with no heading.
- A DOWN monitor lifts out of its group into a strip at the top, above all the groups. The strip is in order of outage start (the start of the open incident), newest first, so a new outage goes to the top. The page uses this order when it loads and when a monitor goes down live. The strip does not show group names. The monitor returns to its place in its group when it recovers.
- Drag a group heading to reorder the groups. Its monitors move with it.
- Drag a monitor row to move it inside its group, into another group, or into the monitors in no group below the groups.
- Both drags save on drop. Each grip also moves with the up and down arrow keys.
- A monitor in the DOWN strip cannot be dragged. Its stored group and position do not change while it is there.
- The rows update live through SSE. No page reload. A state change slides the row between the strip and its group.

### 9.4 Monitor detail

- Large name, status, and "Up for 14 days" or "Down for 6m".
- Four stat tiles: uptime 24h, 7d, 30d, 90d, each by the rule of section 6.1 over its window. Plus average response time. Plus certificate expiry for HTTPS.
- Response time chart: 24h, 7d, 30d toggle.
- 90-day uptime bar.
- Incident list: start, duration, reason.
- Actions: Edit, Pause/Resume, Delete (with confirm).

### 9.5 Add / edit monitor

- One form. The user selects the type first as five large cards with an icon each.
- The form shows only the fields for that type.
- The **Name** field fills itself from the target (for example, `api.example.com`). The user can change it.
- On save, vexil runs the first check at once and shows the result.

### 9.6 Live updates

- The browser opens one SSE connection to `/events`.
- Events: `check` (monitor id, ok, latency), `state` (monitor id, new state).
- The favicon changes color with the overall status (green, red) on the dashboard, the monitor detail page and the public status page. The server builds two SVG icons in the light theme colors, one for all up and one for one or more down. The page links the icon for its state. On the dashboard and the detail page a state change swaps the link between the two. On the public status page the 60-second refresh swaps it, and only public monitors count (see 11). No canvas.
- The built-in logo gets its banner in green or red. An uploaded logo, SVG or PNG, is shown as it is when all are up, and with a red dot in the bottom right corner while one or more monitors are down.
- The icons are SVG, not PNG: on a reload Safari replaces the icon of a page only with an SVG. Every icon link ends with a fragment that is new on each load and each swap, because Safari fetches only an icon URL it has not cached.
- The tab title shows the count of down monitors: "(2) vexil".

---

## 10. Visual design

### 10.1 Direction

Calm. Precise. Like a good instrument panel. Lots of space. Few colors. The status colors are the only loud things on the screen. When everything is up, the screen looks quiet.

References for tone (not for copying): Linear, Vercel dashboard, Stripe status, Apple Health.

### 10.2 Themes

- Light and dark. Follow the system setting by default. A small toggle in the footer overrides it.

### 10.3 Color tokens

| Token | Light | Dark |
|---|---|---|
| `--bg` | `#FAFAFA` | `#0B0D10` |
| `--surface` | `#FFFFFF` | `#12151A` |
| `--border` | `#E8EAED` | `#1F242C` |
| `--text` | `#0F1115` | `#E6E8EB` |
| `--muted` | `#6B7280` | `#8A93A0` |
| `--accent` | `#4F46E5` (brand, changeable) | same |
| `--up` | `#16A34A` | `#22C55E` |
| `--warn` | `#D97706` | `#F59E0B` |
| `--down` | `#DC2626` | `#EF4444` |
| `--paused` | `#9CA3AF` | `#4B5563` |

All text must pass WCAG AA contrast.

### 10.4 Typography

- Font: Inter (variable, OFL license), embedded in the binary. Fallback: system UI stack.
- Numbers: use tabular figures (`font-variant-numeric: tabular-nums`) so values do not jump when they update.
- Scale: 12, 14, 16, 20, 28, 40 px.
- Weights: 400 and 600 only.

### 10.5 Components

- **Status dot.** 8 px. UP: solid green with a slow, soft halo pulse (2.4 s). DOWN: solid red with a faster pulse (1.2 s). PAUSED: gray ring, no fill. PENDING: gray, pulsing. Always add a text label for screen readers.
- **Uptime bar.** Thin rounded segments, 2 px gap, 28 px high. Each segment is one day, colored by its uptime under the rule of section 6.1: 100% = `--up`, 95% to 99.99% = `--warn`, below 95% = `--down`, no data = `--border`. A day with no incident is always green. No data means the monitor did not exist yet or was paused all day. The percentage is cut to two decimals, not rounded, so only a full 100% is green. Hover shows a tooltip: date, uptime, incidents.
- **Response chart.** SVG. A smooth line in `--accent`. An area fill below it from 12% opacity to 0%. A hover crosshair with a value tooltip. No grid lines except a faint baseline.
- **Sparkline.** 80 x 24 px. Same style as the chart, no axes.
- **Cards.** `--surface` background, 1 px `--border`, 12 px radius. No heavy shadows.
- **Buttons.** Primary: `--accent` fill. Secondary: border only. 8 px radius. 36 px high.
- **Inputs.** 40 px high. Clear focus ring in `--accent`.

### 10.6 Motion

- Transitions: 150 ms, ease-out.
- New values fade in. Rows that change state slide to their new position.
- Respect `prefers-reduced-motion`: no pulses, no slides.

### 10.7 Layout

- Maximum content width: 1100 px, centered.
- Mobile first. On narrow screens the dashboard row hides the sparkline, then the uptime bar shrinks to 14 days.

### 10.8 Empty states and errors

- Every empty state has one short sentence and one button. No clip art.
- Error messages say what happened and what to do next.

### 10.9 Style guide page

- Build `/styleguide` (admin only, or dev builds only) first. It shows every component in both themes. Use it to review the design before building features.

---

## 11. Public status page

- One page at `/status`. Shows only monitors with **Show on status page** on.
- Content: brand logo and name, overall status banner, one row per monitor with name and 90-day uptime bar, incidents from the last 14 days.
- Groups: each group shows as a quiet heading with its public monitors below it, in the group order of the dashboard. A group heading shows only when at least one monitor of the group is public. A group with no public monitors is not on the page at all, name included.
- Public monitors in no group go below all the groups, with no heading.
- The DOWN strip of the dashboard does not apply here. Groups keep their order, and a DOWN monitor stays in its group.
- A group has no setting of its own. The **Show on status page** switch of each monitor decides what shows.
- Target URLs and hosts are never shown on the public page. A group name shows as plain, escaped text.
- Custom domain: the user points a domain at vexil through a reverse proxy. The README shows Caddy and nginx examples.
- The page updates itself every 60 seconds (no SSE for the public). A small script fetches the page and replaces only the status content in place, group headings included, so a group appears or disappears with its public monitors: no reload, no focus change. It pauses while the tab is hidden.
- The tab title count and the favicon (see 9.6) count only the public monitors. A private monitor that is down never turns them red. The 60-second refresh updates both with the status content.

---

## 12. White label

Settings page, section **Brand**:

| Setting | Default |
|---|---|
| Product name | vexil |
| Logo (SVG or PNG, max 512 KB) | vexil logo |
| Favicon | Generated from the logo |
| Accent color | `#4F46E5` |
| Show "Powered by vexil" footer | On |

- The product name appears in the page titles, the header, emails and notification footers.
- The code must use the brand setting everywhere. No hard-coded "vexil" in templates or messages. Add a test that searches templates for the literal string.
- The "Powered by" footer and the `reset-password` help on the login page use the product name, not the brand name. They are the only two exceptions to the no-hard-coded-name rule. The name comes from the brand defaults in code, so the templates still have no literal.
- The accent color generates a hover shade and a subtle tint automatically.

---

## 13. Security

- Password: bcrypt. Minimum length 10.
- Session: random 32-byte token in an `HttpOnly`, `SameSite=Lax` cookie. `Secure` when the request is HTTPS. 30-day lifetime. The database stores only a SHA-256 hash of the token.
- A password change (in Settings or with `vexil reset-password`) deletes all other sessions.
- CSRF: use `http.CrossOriginProtection` from the standard library.
- Login rate limit: 5 attempts per minute per IP, and per /64 for IPv6.
- Push tokens: 24 random bytes, URL-safe base64.
- Channel secrets are stored in the database. The UI never shows them again after save (show `••••` with a Replace button).
- Security headers: CSP (self only), `X-Content-Type-Options`, `Referrer-Policy`.

---

## 14. Performance targets

| Target | Value |
|---|---|
| Binary size | Less than 25 MB |
| Memory with 100 monitors | Less than 50 MB |
| Dashboard server response | Less than 50 ms |
| | Measured: 12 ms at a 60-second interval and 15 ms at a 30-second interval, with 100 monitors and 30 days of history, default SQLite cache. |
| Page weight (dashboard, first load) | Less than 200 KB, fonts included |
| Lighthouse accessibility | 100 |

---

## 15. Project layout

```
vexil/
  cmd/vexil/main.go
  internal/
    check/        checkers: http.go, tcp.go, ping.go, dns.go, push.go
    engine/       scheduler, state machine, event hub
    store/        sqlite, queries, migrations/
    notify/       email.go, slack.go, discord.go, telegram.go, ntfy.go, webhook.go
    web/          handlers, sessions, SSE, SVG chart rendering
  web/
    templates/    *.html
    static/       css/, js/, fonts/, img/
  Dockerfile
  README.md
  SPEC.md
  CLAUDE.md
```

---

## 16. Packaging

- `go build ./cmd/vexil` produces the full product.
- Releases: GoReleaser. Targets: linux amd64/arm64, darwin amd64/arm64, windows amd64.
- Docker: multi-stage build, distroless final image, runs as non-root, volume `/data`.
- The distroless image has no `curl`. The binary has a `vexil healthcheck` subcommand. It calls `/readyz` and exits with code 0 or 1. The Dockerfile uses it in `HEALTHCHECK`.
- The binary has a `vexil backup <file>` subcommand, next to `vexil reset-password` and `vexil healthcheck`. It writes one consistent copy of the database to a new file with `VACUUM INTO`, also while the server runs. It does not overwrite a file. It prints the path on success. The image has no `sqlite3` and no shell, so the docs use this command, not a copy of the data folder.
- The README shows a Kubernetes example: `livenessProbe` on `/healthz`, `readinessProbe` on `/readyz`.
- Docs: a systemd unit file example, a `docker run` one-liner, a `docker compose` example.

---

## 17. Testing

- Checkers: table tests against `httptest` servers and local listeners.
- State machine: table tests for every transition.
- Notifications: each channel tested against a fake HTTP server. SMTP tested with a fake server.
- Store: tests against a temporary SQLite file.
- Brand test: no literal product name in templates.
- CI: `go vet`, `staticcheck`, `go test -race`.

---

## 18. Build milestones

Build in this order. Each milestone ends with working, tested code.

1. **Skeleton.** Binary, env config, SQLite, migrations, setup, login, sessions, empty dashboard.
2. **Design system.** CSS tokens, fonts, components, `/styleguide` page in both themes.
3. **Engine.** Five checkers, scheduler, state machine, writer, incidents, tests.
4. **Dashboard and forms.** Monitor list, add/edit/pause/delete, uptime bars, sparklines.
5. **Detail page and live updates.** Charts, stat tiles, incidents, SSE, favicon and title updates.
6. **Notifications.** Six channels, test button, retries, message templates.
7. **Status page and white label.** Public page, brand settings, badges.
8. **Release.** Dockerfile, GoReleaser, README with screenshots, retention job, performance check.

---

## 19. Acceptance criteria for v1

- A new user runs one command and adds a working monitor in less than 60 seconds.
- A DOWN alert arrives within interval + 60 seconds of the outage.
- Every page looks finished in light and dark themes, on desktop and on a phone.
- The Settings page has fewer than 10 fields in total.
- A white-label user can remove every visible trace of the name "vexil" through the UI.
