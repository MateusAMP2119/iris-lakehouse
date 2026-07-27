---
name: pipeline-authoring
description: Author iris pipelines and lanes under the turn protocol — declaration shape, the fetch frame, script-owned pacing (the engine holds NO time), polite scraping tiers, and provenance. Use when writing or reviewing a pipeline script, a source-declaring scraper, an iris-declare.yaml, or a lane composer.
---

# Authoring iris pipelines and lanes

## The one law: the engine holds no time

The iris engine schedules nothing. No intervals, no caps, no backoff, no
cron. Every wait in the whole system lives in exactly one place: **your
pipeline script**, inside its turn. If a pipeline should check something
"every N seconds", that N is a `time.sleep(N)` in the script — declared
nowhere, enforced nowhere — every case its own case.

The engine's side of the deal:

- A **successful turn never parks** — producing or quiet, the engine offers
  the next turn immediately. Your script IS the loop body; pace it yourself.
- A **dead-lettered turn parks** the pipeline until an operator acts
  (replay, manual run, drain). Failure is a worklist item, never a retry loop.
- A **gated pipeline** (`depends_on`) turns only after its upstream produced.
  That's gating, not parking — no pacing needed downstream, the upstream's
  rhythm cascades.

Consequence: **a script that answers instantly-quiet in a loop builds a
CPU fire.** If your turn can end quiet (usually: nothing new), sleep BEFORE
answering or before asking for the next fetch. The sleep is your pace.

## The turn protocol (what your script speaks)

JSON Lines. stdin = engine frames, stdout = your frames, stderr = free log.

Engine sends: `go` (turn opens, echo its number), `row` (input rows from your
declared reads), `run` (input complete — your cue to act), `source` (answer
to your fetch), `res` (answer to your plugin call).

You send: `row` (output, only declared tables/fields), `fetch` (fetch my
declared source NOW), `call` (declared plugin verb), then exactly one
`done`/`error` echoing the turn number.

### The fetch frame — script times, engine fetches

```
you:    {"event":"fetch"}
engine: {"event":"source","url":"...","status":200,"changed":true,"body":"..."}
```

- Only a pipeline declaring a `source:` block may send `fetch`; one at a time.
- The engine does the conditional GET (ETag/If-Modified-Since persist across
  turns), digests the body, records provenance (url, status, sha256) in the
  run capture. Your script never touches the network for its source.
- `changed:false` (304 or identical body) carries no body → answer `done`
  quiet, zero cost anywhere.
- Failure answers `status` 0 (transport) or the HTTP status, no body. YOU
  decide: stay quiet and try next turn, back off longer, or `error` out.
  The engine never retries and never backs off on your behalf.

### The canonical self-paced scraper turn

```python
# after "run" arrives:
if not first_turn:
    time.sleep(CHECK_EVERY_SECONDS)   # your pace, your case
print(json.dumps({"event": "fetch"}), flush=True)
# on the "source" answer:
#   changed → parse, emit rows, done
#   else    → done (quiet)
```

First turn of a session: fetch immediately (catch up on wake), sleep on the
later ones. Sleeping mid-turn is legal and free — the engine has no run
timeout by design; the lane simply waits.

## Choosing a pace (scraping etiquette)

Blocking is about REQUEST RATE, not bytes — a 304 counts like a full
download to a rate limiter. Pick the pace from what the site itself says,
in this order:

1. **HTTP freshness headers** on the source answer (the engine obeys
   validators; your pace should match the origin's `cache-control: max-age`
   when it sends one — big outlets do, usually 30–60s for news sitemaps).
2. **The feed's own declared rhythm**: RSS `<ttl>` (minutes) or WordPress's
   `<sy:updatePeriod>`/`<sy:updateFrequency>`. Boilerplate-prone (a weekly
   paper often claims "hourly") but errs toward harmless over-checking.
3. **Nothing declared** → pick a human-plausible reader pace: 1–5 min for a
   busy site, 15–60 min for a small/local one. Sub-minute sustained polling
   of one origin from one IP reads as a bot.

Never poll faster than the origin's CDN cache anyway — you'd get the same
cached bytes and burn goodwill for nothing.

## Finding the right endpoint for a news source

Never scrape the homepage first. Probe in this order (one curl each):

1. `robots.txt` → `Sitemap:` lines. A **news sitemap** (Google News
   requirement, near-universal for professional outlets) updates within
   ~seconds-minutes of publish and is structured XML: url + publication_date
   + title. Best source, tolerated by design.
2. Common paths when robots hides them: `/sitemap/news.xml`,
   `/sitemaps/news.xml`, `/sitemap_news.xml`, `/feed/news/sitemap.xml`.
3. **RSS/Atom**: `/feed/` (WordPress default), `/rss`. Small, parse-friendly,
   ETag-capable. The standard for small sites.
4. Homepage HTML: last resort — heavy, JS-rendered, bot-walled (DataDome,
   Cloudflare). Expect 403s and captchas.

## Declaration shape (iris-declare.yaml)

```yaml
name: sic_feed
run: [python3, main.py]
lane: news                 # omit → its own lane
logs: {split: true, stamp: true}   # declare the recording contract
source:
  http: https://sicnoticias.pt/sitemap/news.xml
writes:
  - table: news.articles
    fields: [url, published_at, title, site]
```

- `source.http` is the ONLY source field — no interval exists in YAML, ever.
- Declare exact write fields; a row outside them dead-letters the turn.
- Downstream stages use `depends_on: [sic_feed]` + `reads:` — they inherit
  the upstream's rhythm, no pacing of their own.
- A lane composer (`lane:` + `order:`) serializes members; keep one site's
  fetch→transform chain in one lane.

## Provenance

The run capture records what was fed (source url/status/sha256 digest —
never the body) and what you wrote. Carry source lineage into your rows when
it matters downstream: a `source_url` or `fetched_sha` column costs little
and answers "where did this article come from" forever.
