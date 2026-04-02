# Remnawave Observer: Anti-Subscription Sharing System

![Go](https://img.shields.io/badge/Go-1.24-00ADD8?style=for-the-badge&logo=go)
![Docker](https://img.shields.io/badge/Docker-28.0-2496ED?style=for-the-badge&logo=docker)
![Redis](https://img.shields.io/badge/Redis-8.2-DC382D?style=for-the-badge&logo=redis)
![PostgreSQL](https://img.shields.io/badge/PostgreSQL-16-4169E1?style=for-the-badge&logo=postgresql&logoColor=white)
![Panel](https://img.shields.io/badge/Panel%20Ingest-2.7.0-blue?style=for-the-badge)

<p align="center">
  🇷🇺 <a href="README.ru.md">Русский</a> | 🇬🇧 <strong>English</strong>
</p>

## Overview

**Remnawave Observer** is a panel-level anti-sharing service for Remnawave.

It no longer depends on `Vector` by default. The primary data source is **Remnawave Panel 2.7.0+**: Observer polls the panel for per-user IP snapshots, normalizes them into the internal event format, runs scoring, and only then decides whether the user should be disabled.

Current behavior:

- default ingest mode is `panel`
- bans are **scoring-only**
- raw ASN count is **informational only** and does not trigger blocking
- users from configured `Internal Squads` are fully bypassed
- garbage IPs like `0.0.0.0` and `::` are discarded silently and cleaned from Redis/PostgreSQL on startup
- `HWID` and subscription request history (`SRH`) are used as anti-false-positive evidence
- `HWID` evidence is taken only from recently active devices; when no active HWID is available, `SRH` is used as fallback evidence
- legacy `/log-entry` is disabled and returns `410 Gone`

---

## How It Works

```
User -> Xray / Remnawave Node -> Remnawave Panel -> Observer panel poller
                                                      |
                               +----------------------+----------------------+
                               |                                             |
                             Redis                                       PostgreSQL
                  (hot window, sightings, watermarks,            (history, score events,
                   alert cooldowns, reenables)                    anti-abuse actions)
                               |
                               v
                    Anti-Abuse Scoring + Enforcement
                 (DisableUser + optional node-side IP block)
```

Processing flow:

1. Observer polls active nodes through Remnawave panel jobs.
2. `fetch-users-ips` results are normalized into `user_email=<internal numeric ID>` and `source_ip`.
3. Unspecified IPs (`0.0.0.0`, `::`) are dropped immediately.
4. Users from `EXCLUDED_INTERNAL_SQUAD_UUIDS`, `EXCLUDED_USERS`, `EXCLUDED_IPS`, and `EXCLUDED_ASNS` are bypassed.
5. Panel observations are deduplicated by `(nodeUUID, userID, ip, lastSeen)` watermark.
6. A hot ASN window is updated in Redis and the scoring pipeline runs for new provider events.
7. Only `temp_disable` and `hard_disable` trigger enforcement:
   - global `DisableUser` through Remnawave
   - optional temporary node-side IP block through `POST /api/node-plugins/executor`
8. A Redis scheduler re-enables users automatically after `BLOCK_DURATION`.

Important: the executor path does **not** require creating a separate node plugin first. It only requires `Remnawave Panel >= 2.7.0`, `Remnawave Node >= 2.7.0`, and `cap_add: NET_ADMIN` on nodes.

---

## Scoring Model

Observer is now **scoring-only**. Exceeding `MAX_ASNS_PER_USER` does not disable users and does not affect the score directly.

### Base signals

| Signal | Weight | Meaning |
| --- | ---: | --- |
| `GeoFeature` | 55% | Geographic spread between observed IPs |
| `ASNFeature` | 30% | Provider-type mix: mobile lowers risk, VPN/hosting raises it |
| `IPDensityFeature` | 15% | Unique IP density inside non-mobile providers |
| `ProviderMixFeature` | modifier | Raises the floor when VPN/hosting dominates |

### Remnawave evidence

After the base score is calculated, Observer optionally queries Remnawave for:

- `HWID` devices
- subscription request history (`SRH`)

These evidence signals do **not** add an extra penalty. They are used to reduce false positives:

- strong single-device consistency can reduce the final score by `40%`
- low device / agent diversity can reduce the final score by `20%`

Evidence behavior:

- `HWID` uses only recently active devices based on `updatedAt`, or `createdAt` as fallback
- recent-device filtering is controlled by `EVIDENCE_DEVICE_ACTIVITY_WINDOW_DAYS`
- raw device count does not create a separate ban by itself
- if the user has no active `HWID`, Observer falls back to recent `SRH`
- `SRH` is used as confirmation and false-positive damping, not as a standalone blocking reason

This means `HWID` and `SRH` can downgrade a user from `temp_disable` to `warn`, but they do not create an extra ban by themselves.

### Default action thresholds

`SCORE_THRESHOLD_WARN` and `SCORE_THRESHOLD_BLOCK` control the `warn` and `hard_disable` boundaries. With the current defaults:

| Score | Action | Actual effect |
| ---: | --- | --- |
| `< 25` | `none` | No action |
| `25-49` | `monitor` | Persisted and visible in monitoring |
| `50-59` | `warn` | Webhook alert |
| `60-74` | `soft_challenge` | Elevated alert state, no disable |
| `75-84` | `temp_disable` | User disable + optional node-side IP block |
| `85+` | `hard_disable` | User disable + optional node-side IP block |

Low-confidence results are downgraded one level automatically.

---

## Quick Start

### 1. Observer setup

```bash
git clone https://github.com/dd-devgroup/remnawave-observer.git
cd remnawave-observer/observer_conf

cp docker-compose.example.yml docker-compose.yml
```

Minimal `.env`:

```bash
POSTGRES_DSN=postgres://observer:password@postgres:5432/observer?sslmode=disable
REDIS_URL=redis://redis:6379/0

REMNAWAVE_BASE_URL=https://panel.example.com
REMNAWAVE_API_TOKEN=your_api_token_here

BLOCK_DURATION=10m
USER_ASN_TTL_SECONDS=43200
GEOIP_ENABLED=true
SCORE_THRESHOLD_WARN=50.0
SCORE_THRESHOLD_BLOCK=85.0
IP_RESCORING_ENABLED=true
IP_RESCORING_BASE_THRESHOLD=3
IP_RESCORING_DEEP_CHECK_ENABLED=true
IP_RESCORING_DEEP_CHECK_TIMEOUT_SECONDS=10
IP_RESCORING_DEEP_CHECK_RESULT_POLL_SECONDS=2
EVIDENCE_SAFE_DEVICE_COUNT=3
EVIDENCE_DEVICE_GRACE_COUNT=5
EVIDENCE_DEVICE_ACTIVITY_WINDOW_DAYS=30

EXCLUDED_USERS=
EXCLUDED_IPS=
EXCLUDED_INTERNAL_SQUAD_UUIDS=
EXCLUDED_ASNS=

ALERT_WEBHOOK_URL=https://bot.example.com/webhook
```

Start:

```bash
docker compose up -d
docker logs observer -f
```

Prebuilt images:

- `ghcr.io/dd-devgroup/remnawave-observer:dev` tracks the `dev` branch
- `ghcr.io/dd-devgroup/remnawave-observer:latest` tracks the `main` branch
- release builds are also published as version tags like `ghcr.io/dd-devgroup/remnawave-observer:v0.1.0`

### 2. Remnawave requirements

- `Remnawave Panel >= 2.7.0`
- `Remnawave Node >= 2.7.0`
- `cap_add: NET_ADMIN` on nodes if `NODE_EXECUTOR_BLOCK_ENABLED=true`

Observer is now strictly panel-only. `Vector` and HTTP log ingest are no longer supported.

---

## Configuration Reference

### Core

| Variable | Description | Default |
| --- | --- | --- |
| `PORT` | HTTP listen port | `9000` |
| `POSTGRES_DSN` | PostgreSQL connection string | required |
| `REDIS_URL` | Redis URL | `redis://localhost:6379/0` |
| `REMNAWAVE_BASE_URL` | Remnawave panel URL | required |
| `REMNAWAVE_API_TOKEN` | Remnawave API token | required |
| `REMNAWAVE_HEADER` | Optional reverse-proxy gate header in `KEY=VALUE` format | empty |
| `REMNAWAVE_TIMEOUT_SECONDS` | Timeout for Remnawave API requests | `5` |
| `USERID_UUID_CACHE_TTL_HOURS` | Internal ID -> UUID cache TTL | `24` |
| `BLOCK_DURATION` | Disable duration | `5m` |
| `ALERT_WEBHOOK_URL` | Webhook URL for alerts | empty |
| `ALERT_COOLDOWN_SECONDS` | Per-user alert cooldown | `3600` |

### Panel ingest

| Variable | Description | Default |
| --- | --- | --- |
| `PANEL_POLL_INTERVAL_SECONDS` | Main poll interval | `60` |
| `PANEL_FETCH_TIMEOUT_SECONDS` | Timeout for one `fetch-users-ips` job | `20` |
| `PANEL_FETCH_RESULT_POLL_SECONDS` | Result polling interval | `2` |
| `PANEL_FETCH_MAX_INFLIGHT` | Max concurrent fetch jobs | `3` |
| `NODE_EXECUTOR_BLOCK_ENABLED` | Enable temporary node-side IP block | `true` |

### Exclusions and hot window

| Variable | Description | Default |
| --- | --- | --- |
| `EXCLUDED_USERS` | Comma-separated internal user IDs to skip | empty |
| `EXCLUDED_IPS` | Comma-separated IPs or CIDRs to skip | empty |
| `EXCLUDED_INTERNAL_SQUAD_UUIDS` | Internal squad UUIDs fully bypassed by anti-sharing | empty |
| `EXCLUDED_ASNS` | Comma-separated ASNs to skip | empty |
| `USER_ASN_TTL_SECONDS` | TTL of the hot ASN window | `86400` |
| `MAX_ASNS_PER_USER` | Legacy monitoring-only threshold; does not affect score or bans | `4` |
| `CLEAR_IPS_DELAY_SECONDS` | Delay before clearing ASN hot-window state after disable | `30` |

### Scoring and GeoIP

| Variable | Description | Default |
| --- | --- | --- |
| `GEOIP_ENABLED` | Enable GeoIP enrichment | `false` |
| `GEOIP_CACHE_TTL_HOURS` | GeoIP cache TTL | `24` |
| `GEO_FALLBACK_ENABLED` | Enable 2IP fallback | `false` |
| `TWOIP_TOKEN` | 2IP token | empty |
| `TWOIP_BASE_URL` | 2IP API base URL | `https://api.2ip.io` |
| `GEOLITE_ASN_PATH` | GeoLite ASN MMDB path | `/app/data/GeoLite2-ASN.mmdb` |
| `GEOLITE_CITY_PATH` | GeoLite City MMDB path | `/app/data/GeoLite2-City.mmdb` |
| `SCORE_THRESHOLD_WARN` | `warn` threshold | `50` |
| `SCORE_THRESHOLD_BLOCK` | `hard_disable` threshold | `85` |
| `IP_RESCORING_ENABLED` | Enable observe-only rescoring for repeated IP/provider activity | `true` |
| `IP_RESCORING_BASE_THRESHOLD` | Base active-provider threshold before observe-only rescoring starts | `3` |
| `IP_RESCORING_DEEP_CHECK_ENABLED` | Run Remnawave `fetch-users-ips` deep check before observe-only rescoring | `true` |
| `IP_RESCORING_DEEP_CHECK_TIMEOUT_SECONDS` | Timeout for the deep check job | `10` |
| `IP_RESCORING_DEEP_CHECK_RESULT_POLL_SECONDS` | Poll interval for deep check results | `2` |
| `EVIDENCE_SAFE_DEVICE_COUNT` | Informational safe device count used by evidence heuristics | `3` |
| `EVIDENCE_DEVICE_GRACE_COUNT` | Informational grace device count used by evidence heuristics | `5` |
| `EVIDENCE_DEVICE_ACTIVITY_WINDOW_DAYS` | Recent-activity window for HWID and SRH evidence | `30` |

### Optional learning and logging

| Variable | Description | Default |
| --- | --- | --- |
| `UNKNOWN_PROVIDERS_LOG_ENABLED` | Persist unknown provider classifications | `false` |
| `AUTO_LEARNING_ENABLED` | Enable provider auto-learning | `false` |
| `AUTO_LEARNING_INTERVAL_HOURS` | Auto-learning interval | `24` |
| `AUTO_LEARNING_MIN_COUNT` | Min occurrences for learned keyword | `10` |
| `AUTO_LEARNING_MIN_CONFIDENCE` | Min confidence: `high`, `medium`, `low` | `high` |
| `AUTO_LEARNING_MAX_ADDS_PER_RUN` | Max additions per run | `20` |
| `AUTO_LEARNING_OUTPUT_FILE` | Output overlay filename | `providers.learned.yaml` |
| `AUTO_LEARN_MIN_DISTINCT_USERS` | Min distinct users for Postgres-backed learning | `3` |
| `AUTO_LEARN_AUTO_APPROVE_THRESHOLD` | Auto-approve threshold | `0.8` |

---

## Monitoring and Metrics

Every 5 minutes Observer prints a provider hot-window summary to stdout. Provider counts there are **informational only**. The real enforcement decision comes from the latest scoring action.

When observe-only rescoring is enabled, the monitoring summary can also show lines like `Observe-only: 22.2 [none] trigger=ip_threshold deep_check=true`. This indicates a repeated-IP/provider rescore that did not produce enforcement by itself.

Runtime metrics are logged every 60 seconds:

```text
[metrics] requests_total=166842 rejected=0 geoip_ok=1353 geoip_fail=0 geoip_timeout=0
          rw_disable_ok=12 rw_disable_fail=0 rw_enable_ok=8 rw_enable_fail=0
          uuid_cache_hit=450 uuid_cache_miss=50 user_resolve_cache_hit=120 user_resolve_cache_miss=6
          panel_submit_ok=94 panel_submit_fail=0 panel_result_ok=94 panel_result_fail=0 panel_timeout=0 panel_no_data=3 panel_dedup_hit=211
          executor_block_ok=4 executor_block_fail=0 discarded_unspecified_ip=17 excluded_squad_users=25
```

The most useful counters for current deployments:

- `rw_disable_fail`, `rw_enable_fail`: Remnawave enforcement health
- `panel_submit_fail`, `panel_result_fail`, `panel_timeout`: panel ingest issues
- `panel_dedup_hit`: repeated snapshots filtered by watermark
- `discarded_unspecified_ip`: dropped `0.0.0.0` / `::` noise
- `excluded_squad_users`: users bypassed because of internal squad exclusions

---

## Webhook Payload

Alerts are sent for `warn` and above.

```json
{
  "user_identifier": "12345",
  "violation_type": "scoring_action",
  "score": 82.4,
  "score_action": "temp_disable",
  "score_confidence": 0.88,
  "block_duration": "10m",
  "all_user_asns": ["AS31133", "AS3267"],
  "score_modifiers": ["hwid_srh_single_device_consistency"],
  "score_breakdown": [
    { "name": "geo", "score": 90, "weight": 0.55, "confidence": 0.9 },
    { "name": "asn", "score": 70, "weight": 0.30, "confidence": 0.8 },
    { "name": "hwid_evidence", "score": 0, "weight": 0, "confidence": 0.9 }
  ],
  "geo_analysis": {
    "unique_countries": ["RU", "DE"],
    "unique_cities": ["Moscow", "Berlin"],
    "agglomerations": [],
    "max_distance_km": 1608.0,
    "geo_score": 90,
    "geo_flags": ["cross_border"]
  }
}
```

---

## Troubleshooting

**No panel data appears**

Check:

- `REMNAWAVE_BASE_URL` and `REMNAWAVE_API_TOKEN`
- `Remnawave Panel/Node >= 2.7.0`
- `panel_submit_fail`, `panel_result_fail`, `panel_timeout` metrics

**User is not blocked**

Check the latest score action in monitoring or `user_score_events`.

- `monitor`, `warn`, `soft_challenge` do not disable users
- only `temp_disable` and `hard_disable` trigger enforcement

**Some users never enter anti-sharing**

Check:

- `EXCLUDED_INTERNAL_SQUAD_UUIDS`
- `EXCLUDED_USERS`
- `EXCLUDED_IPS`
- `EXCLUDED_ASNS`

Users from excluded internal squads are bypassed before scoring, persistence, alerts, and enforcement.

**`0.0.0.0` or `::` traffic seems missing**

That is expected. Unspecified IPs are discarded on ingest and removed from persisted hot-window/history state during startup cleanup.

**`/log-entry` still receives traffic**

That means some old sender is still alive. Observer no longer accepts HTTP log ingest, so stop and remove the old `Vector` deployment on nodes.

**Node-side IP block does not work**

Check:

- `NODE_EXECUTOR_BLOCK_ENABLED=true`
- node containers have `cap_add: NET_ADMIN`
- `executor_block_fail` metric

---

## System Requirements

Observer server:

- Debian 12+ / Ubuntu 22.04+
- 2 GB RAM
- 1 vCPU
- Docker 24.0+ and Docker Compose v2

Remnawave side:

- `Remnawave Panel >= 2.7.0`
- `Remnawave Node >= 2.7.0`
- `cap_add: NET_ADMIN` if temporary node-side IP blocking is enabled

No separate per-node log collector is required anymore.

---

## License

MIT License
