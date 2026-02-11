# Migration Plan: RabbitMQ + IP-Blocker → Remnawave User Enforcement

**Branch:** `refactor/remnawave-enforcement`
**Goal:** Replace IP-based blocking via RabbitMQ + Blocker service with user-level disable/enable via Remnawave API.

---

## 1. Files/Packages to DELETE

### 1.1 Publisher Package (RabbitMQ)
- `observer/internal/services/publisher/publisher.go`
- `observer/internal/services/publisher/publisher_test.go`
- `observer/internal/services/publisher/publisher_integration_test.go`

### 1.2 BlockMessage Model
- `observer/internal/models/models.go` — remove `BlockMessage` struct (lines 70-82)

### 1.3 Blocker Service (entire directory)
- `blocker/*` — DELETE entire directory
- **Exception:** Preserve example configs in new location:
  - `blocker_conf/examples/vector.toml` (Vector agent config for node)
  - `blocker_conf/examples/docker-compose.node.yml` (Vector deployment snippet)

### 1.4 Config Fields (RabbitMQ-related)
From `observer/internal/config/config.go`, remove:
- `RabbitMQURL`
- `BlockingExchangeName`
- `PublisherPoolSize`
- `RabbitPublishMaxRetries`
- `RabbitPublishBackoffBaseMs`
- `RabbitPublishBackoffMaxMs`
- `PublishConfirmTimeoutMs`
- `MaxIPsPerBlockEvent`

### 1.5 Main Initialization
From `observer/cmd/observer_service/main.go`:
- Lines 46-50: RabbitMQ publisher initialization
- Line 110: `rabbitPublisher` parameter in `NewLogProcessor`
- Line 126: `rabbitPublisher` parameter in `NewServer`

---

## 2. NEW Components to ADD

### 2.1 Enforcement Package
**Location:** `observer/internal/services/enforcement/`

**Interface:**
```go
type Enforcer interface {
    DisableTempByInternalID(ctx context.Context, internalID int64, duration time.Duration, reason string, score int) error
    Ping(ctx context.Context) error
}
```

**Implementation:** `RemnawaveEnforcer` (wraps Remnawave client + Redis scheduler)

### 2.2 Remnawave Client Package
**Location:** `observer/internal/services/remnawave/`

**Methods:**
- `ResolveUUIDByInternalID(ctx, internalID int64) (uuid string, error)`
- `DisableUser(ctx, uuid string) error`
- `EnableUser(ctx, uuid string) error`
- `Ping(ctx) error`

**Dependencies:**
- `github.com/Jolymmiles/remnawave-api-go` (if applicable)
- Fallback: manual HTTP client with `X-Api-Key` auth

### 2.3 Redis Keys for Disable Scheduling
Add to `observer/internal/services/storage/redis.go`:

**New Methods:**
- `GetUserUUIDCache(ctx, internalID) (uuid, bool)`
- `SetUserUUIDCache(ctx, internalID, uuid, ttl)`
- `ScheduleDisable(ctx, internalID, uuid, untilUnix, reason, score) error`
- `PopDueDisables(ctx, nowUnix, limit) ([]internalID, error)`
- `GetDisableRecord(ctx, internalID) (record, ok)`
- `ClearDisableRecord(ctx, internalID) error`

**Key Schema:**
- `rw:uid2uuid:{id}` → uuid (string, TTL cache)
- `rw:disable:{id}` → JSON/Hash (TTL: until + 24h)
- `rw:reenable:zset` → ZSET (member=id, score=untilUnix)

### 2.4 Scheduler Loop (Re-enable)
**Location:** `observer/cmd/observer_service/main.go` (new goroutine)

**Logic:**
```go
ticker := time.NewTicker(REENABLE_TICK_SECONDS)
for {
    select {
    case <-ticker.C:
        ids := PopDueDisables(nowUnix, REENABLE_BATCH_SIZE)
        for _, id := range ids {
            rec := GetDisableRecord(id)
            EnableUser(rec.uuid)
            ClearDisableRecord(id)
        }
    case <-ctx.Done():
        return
    }
}
```

---

## 3. Code Changes: Enforcement Points

### 3.1 Processor: Replace `publishBlockEvent`
**File:** `observer/internal/processor/processor.go`

**OLD (lines 787-825):**
```go
func (p *LogProcessor) publishBlockEvent(ips []string, duration string) error {
    // Chunking logic + publisher.PublishBlockMessage
}
```

**NEW:**
```go
func (p *LogProcessor) disableUser(ctx context.Context, internalID int64, duration time.Duration, reason string, score int) error {
    return p.enforcer.DisableTempByInternalID(ctx, internalID, duration, reason, score)
}
```

**Call Sites (replace all `publishBlockEvent` calls):**
- `processEntryByIP` (line ~400-450)
- `processEntryBySubnet` (line ~500-550)
- `processEntryByASN` (line ~600-650)

**UserEmail Parsing:**
```go
internalID, err := strconv.ParseInt(entry.UserEmail, 10, 64)
if err != nil {
    metrics.RejectedRequests.Add(1) // or new counter: InvalidUserID
    log.Printf("Invalid user_email (not numeric): %q", entry.UserEmail)
    return
}
```

### 3.2 API Server: Remove Publisher Dependency
**File:** `observer/internal/api/server.go`

**Changes:**
- Line 31: Remove `publisher publisher.EventPublisher`
- Line 36: Remove `pub` parameter from `NewServer`
- Lines 169-172: Remove RabbitMQ health check
- Update health response: `{"redis_connection": "ok", "remnawave_connection": "ok"}`

---

## 4. Test Updates

### 4.1 Server Tests
**File:** `observer/internal/api/server_test.go`

**Changes:**
- Remove `capturingPublisher` mock
- Health check assertions: no longer check `rabbitmq_connection`

### 4.2 Processor Integration Tests
**File:** `observer/internal/processor/processor_integration_test.go`

**Changes:**
- Replace `capturingPublisher` with `capturingEnforcer`
- Assertions:
  - OLD: `assert.Len(captured.published, 2)` → chunk count
  - NEW: `assert.Called(enforcer, "DisableTempByInternalID", internalID, ...)`
  - Verify Redis schedule created: `GetDisableRecord(id)` exists

### 4.3 Processor ASN Tests
**File:** `observer/internal/processor/processor_asn_test.go`

**Changes:**
- Same as above: `capturingPublisher` → `capturingEnforcer`

---

## 5. Config Updates

### 5.1 New Environment Variables
Add to `observer/internal/config/config.go`:

**Remnawave Client:**
- `REMNAWAVE_BASE_URL` (required if enforcement enabled)
- `REMNAWAVE_API_TOKEN` (required)
- `REMNAWAVE_TIMEOUT_SECONDS` (default: 5)

**UUID Caching:**
- `USERID_UUID_CACHE_TTL_HOURS` (default: 24)

**Scheduler:**
- `DISABLE_DURATION` (reuse existing `BlockDuration` if present, or rename)
- `REENABLE_TICK_SECONDS` (default: 10)
- `REENABLE_BATCH_SIZE` (default: 100)

### 5.2 Remove RabbitMQ Variables
Delete all `RABBITMQ_*`, `BLOCKING_EXCHANGE_NAME`, `PUBLISHER_POOL_SIZE`, etc.

---

## 6. Documentation

### 6.1 ADR
**File:** `observer/docs/adr/ADR-007-remnawave-disable-instead-of-blocker.md`

**Content:**
- Context: IP-blocking is insufficient (shared IPs, VPN rotation)
- Decision: Enforce at user-level via Remnawave Disable/Enable API
- Consequences: No nftables, no RabbitMQ, simplified stack
- Migration: Scheduler for auto-enable, idempotent disable

### 6.2 Vector Agent Setup
**File:** `observer/docs/VECTOR-AGENT-SETUP.md`

**Content:**
- How to deploy Vector on Xray nodes
- Example `vector.toml`: parse `/var/log/remnanode/xray.{out,err}.log`
- Regex to extract `user_email` (internal numeric ID) and `source_ip`
- POST to `https://observer.pr-dev.pro:38213/log-entry`

---

## 7. Commit Strategy (MIG-1 → MIG-10)

Each commit must:
1. Maintain green tests: `go test -count=1 ./...` (in `observer/`)
2. Be independently revertible
3. Include clear commit message with "what changed"

**Sequence:**
- MIG-1: This plan (no functional changes)
- MIG-2: Add `enforcement` package skeleton + interface
- MIG-3: Remnawave client + tests
- MIG-4: Redis disable-schedule keys + methods
- MIG-5: Real `Enforcer` implementation
- MIG-6: Scheduler loop in `main.go`
- MIG-7: Processor replace `publishBlockEvent`
- MIG-8: API server remove publisher
- MIG-9: Delete RabbitMQ package + configs
- MIG-10: Delete Blocker service + add Vector examples

---

## 8. Acceptance Criteria

- [ ] `go test -count=1 ./...` passes (in `observer/`)
- [ ] No RabbitMQ in runtime (no publisher init, no health check)
- [ ] No Blocker Go service in repo
- [ ] Vector example config + docker-compose snippet present
- [ ] Enforcement via Remnawave API (disable user by internal numeric ID)
- [ ] Auto-enable scheduler functional (Redis-based)
- [ ] ADR-007 documents rationale

---

**Next Step:** Proceed to MIG-2 (enforcement package skeleton).
