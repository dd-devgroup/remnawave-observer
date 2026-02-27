# Remnawave Observer: Anti-Subscription Sharing System

![Go](https://img.shields.io/badge/Go-1.24-00ADD8?style=for-the-badge&logo=go)
![Docker](https://img.shields.io/badge/Docker-28.0-2496ED?style=for-the-badge&logo=docker)
![Redis](https://img.shields.io/badge/Redis-8.2-DC382D?style=for-the-badge&logo=redis)
![PostgreSQL](https://img.shields.io/badge/PostgreSQL-16-4169E1?style=for-the-badge&logo=postgresql&logoColor=white)
![Vector](https://img.shields.io/badge/Vector-0.48-orange?style=for-the-badge)

<p align="center">
  🇷🇺 <a href="README.ru.md">Русский</a> | 🇬🇧 <strong>English</strong>
</p>

## Overview

**Remnawave Observer** automatically detects and blocks users who share their VPN subscriptions.

Unlike simple IP-based blocking (easily bypassed), **Observer blocks at the Remnawave panel level** — violators lose access on **all servers simultaneously**, regardless of which IP they switch to.

**Key features:**

- ✅ Cannot be bypassed by changing IP
- ✅ CGNAT-aware — mobile carriers with 50+ dynamic IPs are not false-positived
- ✅ Multi-signal scoring: geography + provider type + IP density
- ✅ Global blocking and re-enable across all nodes via Remnawave API

---

## How It Works

```
User → Xray → Logs → Vector Agent (node) → Vector Aggregator → Observer
                                                                    │
                                              ┌─────────────────────┤
                                              ▼                     ▼
                                           Redis              PostgreSQL
                                        (ASN pools,          (score history,
                                      reenable schedule)      event log)
                                              │
                                              ▼
                                    Anti-Abuse Scoring
                                  (Geo + ASN type + IP Density)
                                              │
                                              ▼
                                    Remnawave API
                                  (DisableUser / EnableUser)
```

1. **Vector** on each node reads Xray access logs, extracts `user_email` (numeric ID) and `source_ip`
2. **Observer** tracks unique providers (ASNs) per user in a sliding TTL window (default: 12h)
3. **Anti-Abuse Scorer** runs a multi-feature analysis on every new ASN event
4. **On limit/score exceeded** → `DisableUser()` is called — the account is blocked globally
5. **Redis ZSET** schedules automatic re-enable
6. **Scheduler** (every 10s) re-enables expired blocks via `EnableUser()`

---

## Detection

### ASN / Provider Mode

Groups all IPs from the same internet provider (AS number) as a single entity. One legitimate user typically connects from 2–5 providers maximum (home ISP + mobile carrier + maybe work VPN).

**Legitimate user (1 person):**

```
176.59.40.10  → AS12389 (Rostelecom, home)
213.87.120.5  → AS8359  (MTS, mobile)
→ 2 providers ✅
```

**Account sharing (multiple people):**

```
176.59.40.10  → AS12389 (Rostelecom, Moscow)
91.108.4.50   → AS31200 (Beeline, Kazakhstan)
185.22.64.10  → AS48642 (Kyivstar, Ukraine)
→ 3 providers in 3 countries ⚠️ → BLOCK
```

The ASN database auto-downloads from [iptoasn.com](https://iptoasn.com) on startup and refreshes hourly.

---

## Anti-Abuse Scoring

Beyond the hard ASN limit, Observer runs a **multi-feature scoring pipeline** that considers the full picture before taking action. This prevents false positives and enables progressive enforcement.

### Scoring Pipeline

```
ScoringInput
  ├── ASN Classifications   (provider type + modifier per ASN)
  ├── GeoIP Analysis        (countries, cities, max distance between IPs)
  ├── ASN count vs. limit
  └── IPs per ASN           (per-provider IP density)
         │
         ▼
   ┌────────────────────────────────────────────────────┐
   │  GeoFeature        weight=50%  → geographic spread │
   │  ASNFeature        weight=25%  → provider type mix │
   │  CountFeature      weight=15%  → proximity to limit│
   │  IPDensityFeature  weight=10%  → IPs per provider  │
   │  ProviderMixFeature modifier   → VPN/hosting boost │
   └────────────────────────────────────────────────────┘
         │
         ▼
   FinalScore (0–100)  →  Action
```

### Feature Descriptions

| Feature              | Weight   | What it detects                                                                         |
| -------------------- | -------- | --------------------------------------------------------------------------------------- |
| `GeoFeature`         | 50%      | Simultaneous connections from different cities/countries — the strongest sharing signal |
| `ASNFeature`         | 25%      | Mix of high-risk provider types (VPN/hosting raises score, mobile lowers it)            |
| `CountFeature`       | 15%      | How close the user is to the ASN limit                                                  |
| `IPDensityFeature`   | 10%      | High unique IP count per non-mobile provider (CGNAT mobile is excluded entirely)        |
| `ProviderMixFeature` | modifier | If >50% VPN/hosting providers — enforces a minimum score of 50                          |

### IPDensityFeature — Mobile-Aware

Mobile carriers (CGNAT) cause legitimate users to appear with 50+ IPs in a 12h window. `IPDensityFeature` handles this correctly:

| Provider type                | Modifier | IP threshold for score=100      |
| ---------------------------- | -------- | ------------------------------- |
| Mobile (MTS, Megafon)        | ≤ 0.6    | **SKIPPED** — excluded entirely |
| Regional ISP                 | ~0.7     | ~71 IPs                         |
| Fixed ISP (MGTS, Rostelecom) | 1.0      | 50 IPs                          |
| Corporate / Hosting          | 1.5      | ~33 IPs                         |
| VPN / Proxy                  | 1.8      | ~28 IPs                         |

Even at max score, `IPDensityFeature` contributes only **10 points** to the final score. Blocking (score > 75) requires simultaneously high geographic spread.

### Score → Action

| Score | Action           | Effect                       |
| ----- | ---------------- | ---------------------------- |
| < 25  | `none`           | No action                    |
| 25–44 | `monitor`        | Logged, no enforcement       |
| 45–59 | `warn`           | Alert sent                   |
| 60–74 | `soft_challenge` | Temporary access restriction |
| 75–89 | `temp_disable`   | Account suspended            |
| 90+   | `hard_disable`   | Account blocked              |

Low-confidence results are automatically downgraded one level.

---

## Quick Start

### 1. Observer Setup (central server)

```bash
git clone https://github.com/dd-devgroup/remnawave-observer.git
cd remnawave-observer/observer_conf

cp docker-compose.example.yml docker-compose.yml
# Edit .env:
```

**Minimal `.env`:**

```bash
# --- Required ---
REMNAWAVE_BASE_URL=https://panel.example.com
REMNAWAVE_API_TOKEN=your_api_token_here
POSTGRES_DSN=postgres://observer:password@postgres:5432/observer?sslmode=disable
REDIS_URL=redis://redis:6379/0

# --- Detection ---
DETECT_BY_ASN=true
MAX_ASNS_PER_USER=5
USER_ASN_TTL_SECONDS=43200       # 12-hour sliding window

# --- Enforcement ---
BLOCK_DURATION=10m

# --- Scoring (anti-abuse) ---
SCORING_ENABLED=true

# --- GeoIP ---
GEOIP_ENABLED=true

# --- Exclusions ---
EXCLUDED_USERS=admin@example.com,test@example.com
EXCLUDED_IPS=8.8.8.8,1.1.1.1

# --- Notifications ---
ALERT_WEBHOOK_URL=https://bot.example.com/webhook
```

```bash
docker compose up -d
docker logs observer -f
```

### 2. Vector Setup (Xray nodes)

See detailed guide: [**Vector Agent Setup**](observer/docs/VECTOR-AGENT-SETUP.md)

```bash
cd /opt/xray-node
# Copy vector-node.toml and docker-compose.node.yml from observer_conf/vector-examples/
vim vector-node.toml   # set log path + Observer aggregator URL
docker compose -f docker-compose.node.yml up -d
```

**⚠️ Critical:** `user_email` in Xray logs must be the **numeric ID** (int64), not an email address:

```
accepted tcp:1.2.3.4:12345 [inbound_user:12345 >> ...]
                                        ^^^^^ — this must be a number
```

---

## Configuration Reference

### Core

| Variable                 | Description                         | Default                    |
| ------------------------ | ----------------------------------- | -------------------------- |
| `PORT`                   | HTTP listen port                    | `9000`                     |
| `POSTGRES_DSN`           | PostgreSQL connection string        | **required**               |
| `REDIS_URL`              | Redis URL                           | `redis://localhost:6379/0` |
| `REMNAWAVE_BASE_URL`     | Remnawave panel URL                 | **required**               |
| `REMNAWAVE_API_TOKEN`    | Remnawave API bearer token          | **required**               |
| `BLOCK_DURATION`         | Block duration (e.g. `10m`, `1h`)   | `5m`                       |
| `EXCLUDED_USERS`         | Comma-separated user IDs to skip    | —                          |
| `EXCLUDED_IPS`           | Comma-separated IPs to skip         | —                          |
| `EXCLUDED_ASNS`          | Comma-separated ASNs to skip        | —                          |
| `ALERT_WEBHOOK_URL`      | Webhook URL for block notifications | —                          |
| `ALERT_COOLDOWN_SECONDS` | Min seconds between alerts per user | `3600`                     |

### ASN Detection

| Variable                          | Description                        | Default |
| --------------------------------- | ---------------------------------- | ------- |
| `DETECT_BY_ASN`                   | Enable ASN mode                    | `false` |
| `MAX_ASNS_PER_USER`               | Max unique ASNs per user           | `4`     |
| `USER_ASN_TTL_SECONDS`            | Sliding window TTL for ASN records | `3600`  |
| `IPTOASN_UPDATE_INTERVAL_MINUTES` | ASN DB refresh interval            | `60`    |

### GeoIP

| Variable                        | Description                          | Default    |
| ------------------------------- | ------------------------------------ | ---------- |
| `GEOIP_ENABLED`                 | Enable GeoIP analysis                | `false`    |
| `GEOIP_CACHE_TTL_HOURS`         | In-memory GeoIP cache TTL            | `24`       |
| `GEO_FALLBACK_ENABLED`          | Enable 2IP fallback API              | `false`    |
| `TWOIP_TOKEN`                   | 2IP API token                        | —          |
| `GEOLITE_ASN_DOWNLOAD_URL`      | GeoLite2-ASN.mmdb auto-download URL  | —          |
| `GEOLITE_CITY_DOWNLOAD_URL`     | GeoLite2-City.mmdb auto-download URL | —          |
| `GEOLITE_UPDATE_INTERVAL_HOURS` | MMDB auto-update interval            | `168` (7d) |

### Anti-Abuse Scoring

| Variable                | Description                      | Default |
| ----------------------- | -------------------------------- | ------- |
| `SCORING_ENABLED`       | Enable scoring pipeline          | `false` |
| `SCORE_THRESHOLD_WARN`  | Score threshold for warn action  | `45`    |
| `SCORE_THRESHOLD_BLOCK` | Score threshold for block action | `75`    |

### Redis / Scheduler

| Variable                  | Description                         | Default |
| ------------------------- | ----------------------------------- | ------- |
| `CLEAR_IPS_DELAY_SECONDS` | Delay before cleaning up IP records | `30`    |
| `REENABLE_TICK_SECONDS`   | Re-enable scheduler interval        | `10`    |

---

## Monitoring

Every 5 minutes Observer prints an ASN pool summary to stdout:

```
[2026-02-27 20:30:42] === ASN POOLS MONITORING START ===
SUMMARY:
   Total active users: 95
   Near limit: 11
   Over limit: 0

TOP USERS BY PROVIDER COUNT (ASN):
    1. [WARN] 12345
       Providers: 5/5 | TTL: 0.6-12.0h
       Score: 47.4 [monitor]
       ASNs: AS3267(11.6h)[1 IPs], AS31133(0.6h)[2 IPs], AS39264(0.7h)[2 IPs], ...
       Geo: countries: RU, cities: Moscow, Samara, Saint Petersburg
       Details:
          AS31133: 2 IP -> 178.176.87.28 -> RU, Samara (53.21, 50.15) [src:mmdb+2ip]
          AS3267:  1 IP -> 82.179.192.10 -> RU, Moscow  (55.74, 37.61) [src:mmdb+2ip]
```

The **Score** line shows the latest anti-abuse score and current action.

### Runtime Metrics (logged every 60s)

```
[metrics] requests_total=166842 rejected=0 geoip_ok=1353
          rw_disable_ok=12 rw_disable_fail=0 rw_enable_ok=8 rw_enable_fail=0
          uuid_cache_hit=450 uuid_cache_miss=50
```

| Metric            | Alert condition                                 |
| ----------------- | ----------------------------------------------- |
| `rw_disable_fail` | > 5% of disable attempts → check Remnawave API  |
| `rw_enable_fail`  | > 0 → users stuck disabled                      |
| `uuid_cache_miss` | > 20% → increase `USER_ID_UUID_CACHE_TTL_HOURS` |

---

## Webhook Notifications

Observer sends a POST request to `ALERT_WEBHOOK_URL` on every block event.

**ASN mode payload:**

```json
{
	"user_identifier": "12345",
	"limit": 4,
	"block_duration": "10m",
	"violation_type": "asn_limit_exceeded",
	"detected_asn_count": 5,
	"asn_details": {
		"AS31133": {
			"asn": "AS31133",
			"organization": "MTS PJSC",
			"ips": ["185.22.64.15", "91.108.4.22"],
			"ip_count": 2
		}
	}
}
```

---

## Testing

```bash
# Unit tests
cd observer
go test -count=1 ./...

# Integration tests (requires Docker)
docker compose -f docker-compose.test.yml up -d
go test -tags=integration -count=1 ./...
docker compose -f docker-compose.test.yml down
```

---

## Performance

| Metric                   | Value        |
| ------------------------ | ------------ |
| Observer throughput      | ~5 000 req/s |
| Remnawave API latency    | 50–200 ms    |
| Redis latency            | < 5 ms       |
| Vector overhead per node | < 10 MB RAM  |
| Observer RAM             | ~150 MB      |

---

## Security

- Use strong API tokens (32+ characters)
- TLS for Vector → Observer traffic (via nginx)
- Redis isolated in Docker network, not exposed externally
- Configure nginx rate limiting on the Observer endpoint
- Use a secret path for `ALERT_WEBHOOK_URL`

---

## Troubleshooting

**Observer not blocking users:**

```bash
docker logs observer | grep -i "disable\|error"
# Common causes:
# - Invalid REMNAWAVE_API_TOKEN
# - REMNAWAVE_BASE_URL not reachable from container
# - user_email contains email string instead of numeric ID
```

**Vector not sending logs:**

```bash
docker logs vector-agent | grep -i error
# Verify last log line contains a numeric user_email:
docker exec vector-agent tail -1 /var/log/xray/access.log
```

**User not re-enabled:**

```bash
docker logs observer | grep -i "enable\|scheduler"
docker exec redis redis-cli ZRANGE rw:reenable:zset 0 -1 WITHSCORES
```

---

## Documentation

- 📖 [Vector Agent Setup](observer/docs/VECTOR-AGENT-SETUP.md) — detailed per-node deployment guide

---

## System Requirements

**Observer server (central):**

- Debian 12+ / Ubuntu 22.04+
- 2 GB RAM, 1 vCPU
- Docker 24.0+, Docker Compose v2

**Each Xray node:**

- 512 MB RAM, 0.25 vCPU (for Vector)
- Docker 24.0+

---

## License

MIT License
