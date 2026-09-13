"""Seed a demo database for the README screenshots.

Seven monitors, 30 days of raw checks plus 60 more days in the daily table,
a handful of past incidents, and Postgres down right now. The Website
monitor's last check is 61 s old so the engine checks it live at startup
and fills the certificate tile. The other monitors are not due for 5 min.
"""
import base64, random, sqlite3, sys, time
from datetime import datetime, timedelta

db = sys.argv[1]
random.seed(7)
now = int(time.time())
DAY = 86400
RAW_DAYS, TOTAL_DAYS = 30, 90

def ago(days, hh, mm):
    d = (datetime.now() - timedelta(days=days)).replace(hour=hh, minute=mm, second=0, microsecond=0)
    return int(d.timestamp())

# name, type, target, interval_s, public, age_days, base latency ms, jitter, last check age s
monitors = [
    ("Postgres", "tcp", "db.example.com:5432", 300, 0, 90, 6, 2, 5),
    ("Website", "http", "https://www.example.com", 60, 1, 90, 185, 40, 61),
    ("API", "http", "https://api.example.com/health", 300, 1, 90, 120, 30, 5),
    ("Mail", "tcp", "mail.example.com:993", 300, 1, 82, 45, 25, 5),
    ("DNS", "dns", "example.com", 300, 1, 78, 14, 4, 5),
    ("Gateway", "ping", "203.0.113.1", 300, 0, 90, 9, 3, 5),
    ("Nightly backup", "push", "", 86400, 0, 90, None, None, 6 * 3600),
]
# monitor index, start, minutes, reason, status code, open
incidents = [
    (0, now - 25 * 60, None, "connection refused", None),
    (1, ago(42, 15, 10), 7, "HTTP 503", 503),
    (1, ago(66, 10, 41), 12, "timeout", None),
    (2, ago(6, 4, 22), 4, "HTTP 502", 502),
    (2, ago(57, 2, 15), 5, "HTTP 502", 502),
    (3, ago(28, 3, 30), 60, "connection refused", None),
    (5, ago(21, 12, 5), 9, "timeout", None),
]

con = sqlite3.connect(db)
con.execute("PRAGMA journal_mode=WAL")
for t in ("monitors", "checks", "daily", "incidents"):
    con.execute(f"DELETE FROM {t}")

ids = []
for pos, (name, typ, target, iv, pub, age, *_ ) in enumerate(monitors):
    token = base64.urlsafe_b64encode(random.randbytes(24)).decode().rstrip("=") if typ == "push" else None
    cur = con.execute(
        "INSERT INTO monitors (name, type, target, push_token, interval_s, public, paused, position, created_at) VALUES (?,?,?,?,?,?,0,?,?)",
        (name, typ, target, token, iv, pub, pos, now - age * DAY))
    ids.append(cur.lastrowid)

def windows(mi):
    out = []
    for m, start, minutes, reason, code in incidents:
        if m == mi:
            out.append((start, now + 1 if minutes is None else start + minutes * 60, reason, code))
    return out

for mi, (name, typ, target, iv, pub, age, base, jitter, last_age) in enumerate(monitors):
    mid = ids[mi]
    wins = windows(mi)
    def failing(t):
        for s, e, reason, code in wins:
            if s <= t < e:
                return reason, code
        return None
    start = now - age * DAY
    last = now - last_age
    rows = []
    if typ == "push":
        t = last
        while t >= start:
            rows.append((mid, t, 1, None, None, None))
            t -= DAY
    else:
        step = 60  # raw checks every minute for the last 30 days
        lat = base
        t = last
        while t >= max(start, now - RAW_DAYS * DAY):
            lat = max(1, lat + random.uniform(-jitter / 4, jitter / 4))
            if abs(lat - base) > jitter:
                lat = base + (jitter if lat > base else -jitter) * 0.8
            f = failing(t)
            if f:
                rows.append((mid, t, 0, None, f[1], f[0]))
            else:
                rows.append((mid, t, 1, int(lat + random.uniform(-jitter / 3, jitter / 3)), 200 if typ == "http" else None, None))
            t -= step
    con.executemany("INSERT INTO checks (monitor_id, at, ok, latency_ms, status_code, error) VALUES (?,?,?,?,?,?)", rows)
    # Older days go in the daily table.
    first_raw = max(start, now - RAW_DAYS * DAY)
    d = datetime.utcfromtimestamp(start).replace(hour=0, minute=0, second=0)
    while d.timestamp() < first_raw:
        day0 = int(d.timestamp())
        if typ == "push":
            total, ok = 1, 1
        else:
            total = DAY // 60
            down = sum(max(0, min(e, day0 + DAY) - max(s, day0)) for s, e, *_ in wins) // 60
            ok = total - down
        con.execute("INSERT OR REPLACE INTO daily (monitor_id, day, total, ok, avg_latency) VALUES (?,?,?,?,?)",
                    (mid, d.strftime("%Y-%m-%d"), total, ok, base))
        d += timedelta(days=1)

for m, start, minutes, reason, code in incidents:
    con.execute("INSERT INTO incidents (monitor_id, started_at, ended_at, reason) VALUES (?,?,?,?)",
                (ids[m], start, None if minutes is None else start + minutes * 60, reason))
con.commit()
print("checks:", con.execute("SELECT COUNT(*) FROM checks").fetchone()[0], "daily:", con.execute("SELECT COUNT(*) FROM daily").fetchone()[0])
