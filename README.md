# Remnawave Observer: Anti-Subscription Sharing System

![Go](https://img.shields.io/badge/Go-1.24-00ADD8?style=for-the-badge&logo=go)
![Docker](https://img.shields.io/badge/Docker-28.0-2496ED?style=for-the-badge&logo=docker)
![Redis](https://img.shields.io/badge/Redis-8.2-DC382D?style=for-the-badge&logo=redis)
![Vector](https://img.shields.io/badge/Vector-0.48-orange?style=for-the-badge)

<p align="center">
  🇷🇺 <a href="README.ru.md">Русский</a> | 🇬🇧 <strong>English</strong>
</p>

## Overview

**Remnawave Observer** automatically detects and blocks users who share their subscriptions with others.

Unlike IP-based blocking (easily bypassed), **Observer blocks user accounts at the Remnawave panel level**. Violators lose connection **on all servers simultaneously**, regardless of IP address.

**Key features:**

- ✅ Cannot be bypassed by changing IP
- ✅ No false positives (shared IPs don't affect others)
- ✅ Global blocking across all nodes
- ✅ Automatic re-enable after specified time
- ✅ Simple architecture (no message brokers, no per-node agents)

## How It Works

```
User → Xray → Logs → Vector → Observer → Remnawave API (Disable User)
                                    ↓
                                  Redis (schedule auto-enable)
                                    ↓
                              Scheduler → Remnawave API (Enable User)
```

1. **Vector** on each node reads Xray logs, extracts `user_email` (numeric ID) and `source_ip`
2. **Observer** counts unique IPs/subnets/providers per user
3. **On limit exceeded** → calls `Remnawave API DisableUser()` — account disabled globally
4. **Redis ZSET** schedules automatic re-enable
5. **Scheduler** (every 10s) checks expired blocks → calls `EnableUser()`

## Detection Modes

### 1. IP-Based (simple)

Counts unique IP addresses per user.

**Configuration:**

```bash
MAX_IPS_PER_USER=12
```

---

### 2. Subnet-Based (CGNAT-resistant)

Groups IPs by subnets (e.g., `/24`). Prevents false positives from mobile carriers with CGNAT/dynamic IPs.

**Example:**

```
185.22.64.10 + 185.22.64.25 → both in 185.22.64.0/24 → 1 subnet
185.22.64.10 + 91.108.4.50  → different subnets → 2 subnets
```

**Configuration:**

```bash
DETECT_BY_SUBNET=true
MAX_SUBNETS_PER_USER=4
SUBNET_MASK_IPV4=24
```

---

### 3. ASN / Provider-Based (🌟 recommended)

**Most accurate.** Groups all IPs from same internet provider (AS number) as single entity.

**Example of legitimate use (1 person):**

```
176.59.40.10  → AS12389 (Rostelecom home)
213.87.120.5  → AS8359 (MTS mobile)
→ 2 providers ✅
```

**Example of sharing (multiple people):**

```
176.59.40.10  → AS12389 (Rostelecom Moscow)
91.108.4.50   → AS31200 (Beeline Kazakhstan)
185.22.64.10  → AS48642 (Kyivstar Ukraine)
→ 3 providers ⚠️ → BLOCK
```

**Configuration:**

```bash
DETECT_BY_ASN=true
MAX_ASNS_PER_USER=4
```

ASN database auto-downloads from [iptoasn.com](https://iptoasn.com) on startup.

## Quick Start

### 1. Observer Setup (central server)

```bash
git clone https://github.com/dd-devgroup/remnawave-observer.git
cd remnawave-observer/observer_conf

cp .env.example .env
vim .env
```

**Minimal .env:**

```bash
# Detection mode (choose one)
MAX_IPS_PER_USER=12              # IP mode (or)
# DETECT_BY_SUBNET=true           # Subnet mode (or)
DETECT_BY_ASN=true                # ASN mode (recommended)
MAX_ASNS_PER_USER=4

# Remnawave API
REMNAWAVE_BASE_URL=https://panel.example.com
REMNAWAVE_API_TOKEN=your_api_token

# Block duration
BLOCK_DURATION=10m

# Exclusions
EXCLUDED_USERS=admin,test
EXCLUDED_IPS=8.8.8.8

# Webhook (optional)
ALERT_WEBHOOK_URL=https://bot.example.com/webhook
```

```bash
docker-compose up -d
docker logs observer-remna -f
```

### 2. Vector Setup (Xray nodes)

See detailed guide: [**Vector Agent Setup**](observer/docs/VECTOR-AGENT-SETUP.md)

**Quick steps:**

```bash
cd /opt/xray-node
cp observer_conf/vector-examples/* ./
vim vector-node.toml  # Configure log path and Observer URL
docker-compose -f docker-compose.node.yml up -d
```

**⚠️ Critical:** `user_email` in logs must be **numeric ID** (int64), not email:

```
accepted tcp:1.2.3.4:12345 [inbound_user:12345 >> ...]
                                        ^^^^^ — numeric ID
```

## Configuration Reference

### Core Variables

| Variable               | Description                      | Default  |
| ---------------------- | -------------------------------- | -------- |
| `MAX_IPS_PER_USER`     | IP limit                         | 12       |
| `DETECT_BY_SUBNET`     | Enable subnet mode               | false    |
| `MAX_SUBNETS_PER_USER` | Subnet limit                     | 3        |
| `SUBNET_MASK_IPV4`     | Subnet mask                      | 24       |
| `DETECT_BY_ASN`        | Enable ASN mode                  | false    |
| `MAX_ASNS_PER_USER`    | Provider limit                   | 4        |
| `BLOCK_DURATION`       | Block duration                   | 5m       |
| `REMNAWAVE_BASE_URL`   | Panel URL                        | required |
| `REMNAWAVE_API_TOKEN`  | API token                        | required |
| `EXCLUDED_USERS`       | Excluded users (comma-separated) | —        |
| `EXCLUDED_IPS`         | Excluded IPs                     | —        |
| `EXCLUDED_SUBNETS`     | Excluded subnets                 | —        |
| `EXCLUDED_ASNS`        | Excluded ASNs                    | —        |
| `ALERT_WEBHOOK_URL`    | Webhook URL                      | —        |

### Redis Settings

| Variable                  | Description        | Default |
| ------------------------- | ------------------ | ------- |
| `USER_IP_TTL_SECONDS`     | Record TTL         | 3600    |
| `CLEAR_IPS_DELAY_SECONDS` | Cleanup delay      | 30      |
| `REENABLE_TICK_SECONDS`   | Scheduler interval | 10      |

## Webhook Notifications

Observer sends POST requests to `ALERT_WEBHOOK_URL` when users are blocked.

### Payload Examples

**ASN mode:**

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

**IP mode:**

```json
{
	"user_identifier": "12345",
	"detected_ips_count": 13,
	"limit": 12,
	"all_user_ips": ["1.1.1.1", "2.2.2.2"],
	"block_duration": "10m",
	"violation_type": "ip_limit_exceeded"
}
```

**Subnet mode:**

```json
{
	"user_identifier": "12345",
	"detected_ips_count": 4,
	"limit": 3,
	"all_user_ips": ["185.22.64.0/24", "91.108.4.0/24"],
	"block_duration": "10m",
	"violation_type": "subnet_limit_exceeded"
}
```

## Monitoring

### Metrics (logged every 60s)

```
[Metrics] requests_total=1523 rejected=5 geoip_ok=1480
[Metrics] rw_disable_ok=12 rw_disable_fail=0 rw_enable_ok=8 rw_enable_fail=0
[Metrics] uuid_cache_hit=450 uuid_cache_miss=50
```

**Key metrics:**

- `rw_disable_ok/fail` — user blocks (success/failure)
- `rw_enable_ok/fail` — auto re-enables (success/failure)
- `uuid_cache_hit/miss` — cache efficiency (target: >80% hits)
- `rejected` — invalid requests

### Alerts

```
rw_disable_fail / (rw_disable_ok + rw_disable_fail) > 0.05  # >5%
→ Check Remnawave API

rw_enable_fail > 0
→ Users stuck disabled!

uuid_cache_miss / (uuid_cache_hit + uuid_cache_miss) > 0.2  # >20%
→ Increase TTL
```

## Testing

```bash
# Unit tests
cd observer
go test -count=1 ./...

# Integration tests
docker-compose -f docker-compose.test.yml up -d
go test -tags=integration -count=1 ./...
docker-compose -f docker-compose.test.yml down
```

**Coverage:** 75+ tests across all packages ✅

## Performance

| Metric                | Value           |
| --------------------- | --------------- |
| Observer throughput   | ~5000 req/s     |
| Remnawave API latency | 50-200 ms       |
| Redis latency         | <5 ms           |
| Vector overhead       | <10 MB RAM/node |
| Observer RAM          | ~150 MB         |

### Optimization

**High-load systems:**

```toml
# vector-node.toml
batch.max_events = 500
batch.timeout_secs = 2
compression = "gzip"
```

**Horizontal scaling:**

```bash
# Run multiple Observer instances behind load balancer
# Use single shared Redis for all instances
```

## Security

**Recommendations:**

- Use strong API tokens (32+ chars)
- HTTPS for Vector → Observer
- Redis isolated in Docker network
- Nginx rate limiting
- Secret webhook URLs

**Minimal permissions:**

- Observer: no root required
- Vector: read-only log mounts
- Redis: not exposed externally

## Troubleshooting

### Observer not blocking users

```bash
# Check API
curl -H "Authorization: Bearer TOKEN" https://panel.example.com/api/users/1

# Check logs
docker logs observer-remna | grep -i disable

# Common causes:
# - Invalid REMNAWAVE_API_TOKEN
# - API unreachable
# - user_email contains email instead of numeric ID
```

### Vector not sending data

```bash
docker logs vector-agent | grep -i error
docker exec vector-agent tail -1 /var/log/xray/access.log
# Should show user_email as number!
```

### User not re-enabled

```bash
docker logs observer-remna | grep Scheduler
docker exec redis-obs redis-cli ZRANGE rw:reenable:zset 0 -1 WITHSCORES
docker logs observer-remna | grep rw_enable_fail
```

## Documentation

- 📖 [Vector Agent Setup Guide](observer/docs/VECTOR-AGENT-SETUP.md) — detailed node deployment
- 🏗️ [ADR-007: Architecture](observer/docs/adr/ADR-007-remnawave-user-enforcement.md) — design rationale
- 🧪 [N8N Updater Playbook](observer/docs/N8N-UPDATER-PLAYBOOK.md) — geodata automation

## System Requirements

**Observer (central):**

- Debian 13 / Ubuntu 24.04
- 2 GB RAM, 1 vCPU
- Docker 28.0+

**Node (each Xray server):**

- Debian 13 / Ubuntu 24.04
- 1 GB RAM, 0.25 vCPU (Vector)
- Docker 28.0+

## License

MIT License
