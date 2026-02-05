# ADR 005 — CAIDA AS2Org + AutoLearner overlay for provider classification

## Status

Accepted (Commits G–K)

## Context

The AutoLearner watches `unknown_providers.json` and automatically promotes
frequently-seen organisations into `providers.yaml` keyword lists so that the
ASN classifier can assign the correct provider type (hosting, vpn_proxy, isp,
etc.) without manual intervention.

Two problems existed before this work:

1. **Read-only config directory** — in the Docker deployment `providers.yaml`
   lives in a read-only mount (`/app/config`).  The old AutoLearner tried to
   overwrite that file, silently failing in production.

2. **Noisy auto-adds** — iptoasn organisation names are inconsistent (e.g.
   `AS34533-ESAMARA`, `AS34533 Samara Telecom`).  With no external
   normalisation source many providers were either missed (confidence too low)
   or incorrectly typed (heuristic false-positives on short/generic names).

## Decision

### 1. Overlay file pattern

`providers.yaml` (base, read-only, `configDir`) is never modified.  The
AutoLearner writes learned keywords to a second file —
`providers.learned.yaml` (overlay, writable, `dataDir`).  At startup and after
every reload `GeoDataLoader.loadProvidersWithOverlay()` reads the base file,
then appends all overlay keywords per type.  The overlay uses the same
`ProvidersConfig` YAML schema as the base.

Writes are atomic: content is written to a `.tmp` sibling first, then
`os.Rename`d into place (single syscall, crash-safe on the same filesystem).

### 2. CAIDA AS-Organizations lookup

CAIDA publishes a daily snapshot that maps every BGP ASN to a stable
organisation name and country.  `AS2OrgLoader` downloads the gzip file on
first start, caches it in `dataDir`, and refreshes it on a configurable ticker.
Parsing is done once per refresh; the result is two in-memory maps
(`asnMap`, `orgMap`) swapped under a write lock (copy-on-write, readers never
block each other).

### 3. Dual-source classification

For each candidate in `unknown_providers.json` the AutoLearner now runs
`suggestProviderType` **twice**: once against the iptoasn org name, once
against the CAIDA org name (looked up via the provider's recorded ASN).  The
result with the higher confidence rank wins.  This way a provider like
`AS88888-RAND` that carries no signal in iptoasn can still be correctly typed
as `vpn_proxy` if the CAIDA record reads `VPNCloud Server Ltd`.

Crucially the **keyword** that is written to the overlay is always extracted
from the iptoasn org name — that is the string that will actually appear in
future traffic, so the keyword must be a substring of it.

### 4. Anti-spam guard

If CAIDA is unavailable (download failed / disabled) **and** the iptoasn org
name produces no keyword or heuristic match (evidence == `"default"`), the
provider is skipped entirely regardless of observation count.  This prevents
garbage keywords like `"as"` or `"ltd"` from being auto-added during CAIDA
outages.

### 5. Improved keyword extraction

`extractKeyword` now iterates all words in the org name, skipping a
stop-word list (`llc`, `ltd`, `inc`, `pjsc`, `ojsc`, `corp`, `network`,
`communications`, `group`, `holding`, `holdings`, `company`, `corporation`)
and stripping known ASN suffixes (`-as`, `-net`, `-isp`) when the remainder is
at least three characters.

### 6. Per-cycle rate limit

`AUTO_LEARNING_MAX_ADDS_PER_RUN` (default 20) caps the number of new keywords
written in a single cycle.  Together with the anti-spam guard this prevents a
burst of bad data from polluting the overlay.

## Consequences

- `configDir` is never written to; the overlay pattern is compatible with
  read-only container mounts.
- CAIDA normalisation dramatically reduces false-positive and missed-provider
  rates.  During CAIDA outages the anti-spam guard degrades gracefully (only
  high-confidence iptoasn matches are added).
- The overlay file is a plain YAML diff that can be reviewed, version-controlled,
  or rolled back by simply deleting it.
- Two HTTP downloads at startup (iptoasn + CAIDA); CAIDA is ~20 MB gzip.
  Both are cached locally and only re-downloaded on a configurable interval.

## Env vars

| Variable | Default | Description |
|---|---|---|
| `GEODATA_CONFIG_DIR` | `/app/config` | Read-only base config (providers.yaml) |
| `GEODATA_DATA_DIR` | `/app/data` | Writable data dir (overlay, unknown_providers.json, CAIDA cache) |
| `AUTO_LEARNING_ENABLED` | `false` | Master switch for AutoLearner |
| `AUTO_LEARNING_INTERVAL_HOURS` | `24` | Cycle interval |
| `AUTO_LEARNING_MIN_COUNT` | `10` | Min observations before consideration |
| `AUTO_LEARNING_MIN_CONFIDENCE` | `high` | Min confidence to accept (`high` / `medium` / `low`) |
| `AUTO_LEARNING_MAX_ADDS_PER_RUN` | `20` | Max new keywords per cycle |
| `AUTO_LEARNING_OUTPUT_FILE` | `providers.learned.yaml` | Overlay filename in dataDir |
| `CAIDA_ENABLED` | `true` | Download and use CAIDA AS2Org |
| `CAIDA_DOWNLOAD_URL` | *(upstream default)* | Override CAIDA gzip URL |
| `CAIDA_REFRESH_HOURS` | `168` | CAIDA refresh interval (7 days) |
