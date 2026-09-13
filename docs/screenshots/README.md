# README screenshots

The four screenshots come from a seeded demo database, so they can be retaken after a UI change.

`seed.py` fills a database with seven monitors, 30 days of raw checks, 60 more days of daily totals and a few incidents, all relative to the time it runs. `shoot.py` logs in, takes the four shots at 2x with headless Chromium and shrinks them to 256 colors. `acme.svg` is the logo for the status page shot.

Needs Python 3 with `playwright` and `pillow`:

```
python3 -m venv .venv && .venv/bin/pip install playwright pillow && .venv/bin/playwright install chromium
```

Steps, from the repository root:

```
go build ./cmd/vexil
rm -rf /tmp/vexil-demo && mkdir /tmp/vexil-demo
printf 'correct-horse-battery\ncorrect-horse-battery\n' | VEXIL_DATA=/tmp/vexil-demo ./vexil reset-password
python3 docs/screenshots/seed.py /tmp/vexil-demo/vexil.db
VEXIL_ADDR=127.0.0.1:18080 VEXIL_DATA=/tmp/vexil-demo VEXIL_BASE_URL=https://status.example.com ./vexil &
sleep 4
python3 docs/screenshots/shoot.py
```

Run `shoot.py` within a minute of starting the server. The Website monitor gets one live check at startup, which fills the certificate tile. The other monitors are not due for five minutes, so nothing else changes while the shots are taken.
