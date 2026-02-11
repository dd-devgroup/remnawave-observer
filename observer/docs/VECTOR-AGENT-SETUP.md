# Vector Agent Setup Guide

**Goal:** Deploy Vector agent on Xray nodes to collect access logs and send them to Observer service for user-level enforcement.

---

## Overview

After migration from IP-blocking (RabbitMQ + Blocker) to user-level enforcement (Remnawave API), nodes no longer need the Blocker Go service. Instead, use **Vector** — a lightweight log collection agent that:

- Parses Xray access logs
- Extracts `user_email` (internal numeric ID) and `source_ip`
- Sends structured data to Observer via HTTP POST
- Handles batching, retries, and disk buffering

---

## Architecture

```
┌─────────────────────────────────────────────┐
│ Xray Node                                   │
│  ┌──────────┐         ┌─────────────────┐  │
│  │  Xray    │ logs    │  Vector Agent   │  │
│  │  Server  │────────▶│  (container)    │  │
│  └──────────┘         └─────────────────┘  │
│                              │              │
└──────────────────────────────┼──────────────┘
                               │ HTTPS POST
                               ▼
                  ┌────────────────────────┐
                  │  Observer Service      │
                  │  (observer.pr-dev.pro) │
                  └────────────────────────┘
                               │
                               ▼
                  ┌────────────────────────┐
                  │  Remnawave Panel       │
                  │  (Disable User API)    │
                  └────────────────────────┘
```

---

## Prerequisites

1. **Xray/V2Ray** configured to write access logs to file:
   ```json
   {
     "log": {
       "access": "/var/log/xray/access.log",
       "loglevel": "info"
     }
   }
   ```

2. **Docker + Docker Compose** installed on node

3. **Network access** to Observer service (HTTPS outbound to `observer.pr-dev.pro:38213`)

---

## Deployment Steps

### 1. Download Configuration Files

From repository `observer_conf/vector-examples/`:
- `vector-node.toml` — Vector configuration
- `docker-compose.node.yml` — Docker Compose file

Copy to node (e.g., `/opt/vector-agent/`):
```bash
mkdir -p /opt/vector-agent
cd /opt/vector-agent
# Upload vector-node.toml and docker-compose.node.yml
```

### 2. Customize vector-node.toml

**Important:** Adjust log path and regex to match your Xray log format.

#### Example Xray Log Formats

**Format 1: Standard Xray (email in metadata)**
```
2026/02/11 20:00:00 accepted tcp:1.2.3.4:12345 [email:user@example.com >> proxy.example.com:443]
```
Regex:
```toml
parsed = parse_regex!(.message, r'accepted tcp:(?P<source_ip>[0-9.]+):[0-9]+ \[email:(?P<user_email>[^\s]+)')
```

**Format 2: Remnawave Panel (numeric ID)**
```
2026/02/11 20:00:00 accepted tcp:1.2.3.4:12345 [inbound_user:12345 >> proxy.example.com:443]
```
Regex:
```toml
parsed = parse_regex!(.message, r'accepted tcp:(?P<source_ip>[0-9.]+):[0-9]+ \[inbound_user:(?P<user_email>[0-9]+)')
```

**⚠️ Critical:** `user_email` field **must** contain the internal numeric ID (int64), not an email address. Observer expects this format after MIG-7.

#### Update Log Path

Change `include` path to match your setup:
```toml
[sources.xray_access_logs]
  include = ["/var/log/xray/access.log"]
  # OR for remnanode:
  # include = ["/var/log/remnanode/xray.out.log"]
```

#### Update Observer URL

If using a different Observer endpoint:
```toml
[sinks.observer_service]
  uri = "https://your-observer.example.com:38213/log-entry"
```

### 3. Adjust docker-compose.node.yml

Mount the correct log directory:
```yaml
volumes:
  - /var/log/xray:/var/log/xray:ro  # Adjust path
```

Optional: Set resource limits based on node capacity:
```yaml
deploy:
  resources:
    limits:
      cpus: '0.5'      # 50% of one core
      memory: 512M
```

### 4. Start Vector Agent

```bash
cd /opt/vector-agent
docker-compose -f docker-compose.node.yml up -d
```

Verify it's running:
```bash
docker ps | grep vector-agent
docker logs vector-agent --tail 50
```

### 5. Test Configuration

Validate Vector config:
```bash
docker exec vector-agent vector validate /etc/vector/vector.toml
```

Check if logs are being parsed:
```bash
docker logs -f vector-agent
```

You should see:
```
2026-02-11T20:00:00.000Z  INFO vector: Healthcheck passed.
2026-02-11T20:00:05.000Z  INFO sink{name=observer_service}: Sent batch of 100 events
```

### 6. Verify Observer Reception

Check Observer logs:
```bash
docker logs observer | grep "POST /log-entry"
```

Or query Observer health:
```bash
curl https://observer.pr-dev.pro:38213/health
```

---

## Monitoring

### Key Metrics to Watch

1. **Vector metrics** (if enabled via Prometheus exporter):
   - `vector_component_received_events_total{component_name="xray_access_logs"}`
   - `vector_component_sent_events_total{component_name="observer_service"}`
   - `vector_component_errors_total{component_name="observer_service"}`

2. **Disk buffer usage**:
   ```bash
   du -sh /opt/vector-agent/vector-data
   ```
   Should stay below 256 MB (configured limit).

3. **Observer metrics** (from Observer logs):
   - `requests_total` — should increase
   - `rejected=0` — no rejected requests
   - `rw_disable_ok` / `rw_disable_fail` — enforcement success/failure

### Health Check Endpoint

Vector checks Observer health every 30s:
```toml
healthcheck.enabled = true
healthcheck.uri = "https://observer.pr-dev.pro:38213/health"
```

If health check fails, Vector will:
- Log warnings
- Continue buffering events to disk
- Retry when service recovers

---

## Troubleshooting

### Problem: Vector not sending events

**Check 1:** Verify log path is correct and readable:
```bash
docker exec vector-agent ls -la /var/log/xray/access.log
# Should show file with recent timestamp
```

**Check 2:** Test regex manually:
```bash
# View sample log line
docker exec vector-agent tail -1 /var/log/xray/access.log
# Compare with regex in vector-node.toml
```

**Check 3:** Check Vector internal logs:
```bash
docker logs vector-agent 2>&1 | grep -i error
```

### Problem: 413 Request Entity Too Large

Observer has limits:
- `MAX_REQUEST_BYTES=2MB` (default)
- `MAX_LOG_ENTRIES_PER_REQUEST=1000` (default)

Reduce Vector batch size in `vector-node.toml`:
```toml
batch.max_events = 50  # Reduce from 100
```

### Problem: High disk buffer usage

Events are buffering because Observer is unreachable or slow.

**Check network connectivity:**
```bash
docker exec vector-agent wget -O- https://observer.pr-dev.pro:38213/health
```

**Check buffer size:**
```bash
du -sh /opt/vector-agent/vector-data
```

If approaching 256 MB limit, Vector will drop newest events (`when_full = "drop_newest"`).

### Problem: Missing user_email field

Observer will reject entries without `user_email`.

**Validate parsed output:**
Enable Vector debug mode in `docker-compose.node.yml`:
```yaml
environment:
  - VECTOR_LOG=debug
```

Restart and check logs:
```bash
docker-compose -f docker-compose.node.yml restart
docker logs -f vector-agent | grep "user_email"
```

If regex doesn't match, update the pattern in `vector-node.toml`.

---

## Performance Tuning

### For High-Traffic Nodes (>10k req/s)

1. **Increase batch size** (reduces HTTP overhead):
   ```toml
   batch.max_events = 500
   batch.timeout_secs = 2
   ```

2. **Enable compression**:
   ```toml
   [sinks.observer_service]
     compression = "gzip"
   ```

3. **Increase buffer size**:
   ```toml
   buffer.max_size = 536870912  # 512 MB
   ```

4. **Adjust resource limits**:
   ```yaml
   deploy:
     resources:
       limits:
         cpus: '1.0'
         memory: 1G
   ```

### For Low-Traffic Nodes (<100 req/s)

1. **Reduce batch timeout** (faster delivery):
   ```toml
   batch.timeout_secs = 1
   ```

2. **Smaller buffer**:
   ```toml
   buffer.max_size = 67108864  # 64 MB
   ```

---

## Security Considerations

1. **HTTPS only:** Vector → Observer communication uses TLS
2. **Read-only log mounts:** Logs mounted as `:ro` in Docker
3. **No credentials in logs:** Vector config doesn't log sensitive data
4. **Disk buffer encryption:** Consider encrypting `/opt/vector-agent/vector-data` volume
5. **Firewall:** Only allow outbound HTTPS to Observer IP

---

## Rollback to Blocker (Legacy)

If you need to revert to the old IP-blocking system:

1. Stop Vector:
   ```bash
   docker-compose -f docker-compose.node.yml down
   ```

2. Re-deploy Blocker Go service (from `blocker/` directory before MIG-10)

3. Reconfigure Observer to publish to RabbitMQ (revert MIG-7-9)

**Note:** This is not recommended. User-level enforcement is superior to IP-blocking.

---

## Example: Complete Deployment

```bash
# On node (as root)
cd /root
git clone https://github.com/your-org/remnawave-observer.git
cd remnawave-observer/observer_conf/vector-examples

# Customize config
nano vector-node.toml  # Adjust log path + regex

# Deploy
mkdir -p /opt/vector-agent
cp vector-node.toml docker-compose.node.yml /opt/vector-agent/
cd /opt/vector-agent
docker-compose -f docker-compose.node.yml up -d

# Verify
docker logs -f vector-agent
curl https://observer.pr-dev.pro:38213/health

# Monitor for 5 minutes
watch -n5 'docker logs vector-agent --tail 10'
```

---

## References

- [Vector Documentation](https://vector.dev/docs/)
- [Vector Remap Transform](https://vector.dev/docs/reference/vrl/)
- [Observer API Docs](../internal/api/server.go) — `/log-entry` endpoint
- [ADR-007](adr/ADR-007-remnawave-user-enforcement.md) — User-level enforcement architecture

---

**Questions?** File an issue at [remnawave-observer/issues](https://github.com/your-org/remnawave-observer/issues)
