"""quake_report: healthy-lane aggregator under the Iris turn protocol.

Gates on quake_feed through depends_on and declares reads on demo.quakes, so
each turn the engine feeds exactly the quake rows that are NEW OR REVISED since
this pipeline's last consumed position -- incremental processing for free, no
bookkeeping in the pipeline. The turn folds that batch into summary metrics and
answers with row frames for demo.quake_report (metric names are primary keys,
so a re-run refreshes the same rows), then the done terminal.

stdout is protocol-only; all logging goes to stderr.
"""

import json
import sys


def log(msg: str) -> None:
    print(f"quake_report: {msg}", file=sys.stderr, flush=True)


def emit(frame: dict) -> None:
    sys.stdout.write(json.dumps(frame) + "\n")
    sys.stdout.flush()


def metrics_for(batch: list[dict]) -> dict[str, str]:
    quakes = [r for r in batch if r.get("event_type") == "earthquake"]
    out = {"quakes_in_last_batch": str(len(quakes))}
    if not quakes:
        out["strongest_in_last_batch"] = "none in batch"
        return out

    def place(r: dict) -> str:
        return r.get("place") or "unknown location"

    with_mag = [r for r in quakes if r.get("magnitude") is not None]
    if with_mag:
        top = max(with_mag, key=lambda r: r["magnitude"])
        out["strongest_in_last_batch"] = f"M{top['magnitude']} -- {place(top)}"
        avg = sum(r["magnitude"] for r in with_mag) / len(with_mag)
        out["avg_magnitude_in_last_batch"] = f"{avg:.2f}"
    with_depth = [r for r in quakes if r.get("depth_km") is not None]
    if with_depth:
        deep = max(with_depth, key=lambda r: r["depth_km"])
        out["deepest_in_last_batch"] = f"{deep['depth_km']} km -- {place(deep)}"
    latest = max(quakes, key=lambda r: r.get("occurred_at") or "")
    out["latest_event"] = f"{latest.get('occurred_at')} -- {place(latest)}"
    reviewed = sum(1 for r in quakes if r.get("status") == "reviewed")
    out["reviewed_share_of_batch"] = f"{100 * reviewed // len(quakes)}%"
    return out


def main() -> None:
    turn = None
    batch: list[dict] = []
    for line in sys.stdin:
        frame = json.loads(line)
        if frame["event"] == "go":
            turn = frame["turn"]
            batch = []
        elif frame["event"] == "row":
            batch.append(frame["row"])
        elif frame["event"] == "run":
            for metric, value in metrics_for(batch).items():
                emit({"event": "row", "table": "demo.quake_report", "row": {"metric": metric, "value": value}})
            log(f"answered turn {turn}: folded {len(batch)} fed rows into the report")
            emit({"event": "done", "turn": turn})


if __name__ == "__main__":
    main()
