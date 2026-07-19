"""quake_feed: healthy-lane extractor under the Iris turn protocol.

A resident pipeline: the engine writes go/run frames to stdin, and each turn
this process pulls the last 24 hours of earthquakes from the USGS public CSV
feed (no API key) and answers with one row frame per quake for demo.quakes,
then the done terminal. The engine owns every database access: rows are
upserted by primary key (the USGS event id), so re-runs refresh revised
magnitudes and statuses -- USGS keeps re-estimating for hours after a quake,
and every revision lands as a new provenance stamp.

stdout is protocol-only; all logging goes to stderr. An unreachable feed
degrades to a quiet turn (zero rows, still done): healthy lanes degrade, they
never fail.
"""

import csv
import io
import json
import sys
import urllib.request

FEED_URL = "https://earthquake.usgs.gov/earthquakes/feed/v1.0/summary/all_day.csv"


def log(msg: str) -> None:
    print(f"quake_feed: {msg}", file=sys.stderr, flush=True)


def emit(frame: dict) -> None:
    sys.stdout.write(json.dumps(frame) + "\n")
    sys.stdout.flush()


def fetch_rows() -> list[dict]:
    """Fetch and shape the feed; an unreachable or malformed feed yields []."""
    try:
        with urllib.request.urlopen(FEED_URL, timeout=30) as resp:
            body = resp.read().decode("utf-8", errors="replace")
    except OSError as exc:
        log(f"USGS feed unreachable ({exc}); keeping existing rows and staying healthy")
        return []

    rows = []
    for rec in csv.DictReader(io.StringIO(body)):
        if not rec.get("id"):
            continue
        row = {
            "id": rec["id"],
            "occurred_at": rec.get("time") or None,
            "latitude": float(rec["latitude"]) if rec.get("latitude") else None,
            "longitude": float(rec["longitude"]) if rec.get("longitude") else None,
            "depth_km": float(rec["depth"]) if rec.get("depth") else None,
            "magnitude": float(rec["mag"]) if rec.get("mag") else None,
            "place": rec.get("place") or None,
            "event_type": rec.get("type") or None,
            "status": rec.get("status") or None,
        }
        if row["occurred_at"] is None:
            continue
        rows.append(row)
    return rows


def main() -> None:
    turn = None
    for line in sys.stdin:
        frame = json.loads(line)
        if frame["event"] == "go":
            turn = frame["turn"]
        elif frame["event"] == "run":
            rows = fetch_rows()
            for row in rows:
                emit({"event": "row", "table": "demo.quakes", "row": row})
            log(f"answered turn {turn} with {len(rows)} quakes from the last 24h")
            emit({"event": "done", "turn": turn})


if __name__ == "__main__":
    main()
