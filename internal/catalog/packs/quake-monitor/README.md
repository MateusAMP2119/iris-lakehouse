# quake-monitor

The starter demo: a healthy two-member lane speaking the turn protocol.

- `quake_feed` pulls recent USGS earthquakes and upserts them into `demo.quakes`.
- `quake_report` reads the feed rows and derives `demo.quake_report` metrics.

Install and run:

```
iris catalog init --apply
iris ps
```

Pack conventions (all packs follow these):

- No secrets in pack files; no `env_file` entries pointing at files the pack does not carry.
- Workers speak frames on stdout (turn protocol) and log free-form on stderr.
- Declared tables live under `schemas/`; the pack never touches engine-owned surfaces.
