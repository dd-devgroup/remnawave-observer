# ADR-007: User-Level Enforcement via Remnawave API (Instead of IP Blocking)

**Status:** Accepted
**Date:** 2026-02-11
**Deciders:** Architecture team
**Related:** MIG-1 through MIG-10 (refactor/remnawave-enforcement branch)

---

## Context

### Previous Architecture (IP-Level Blocking)

**Components:**
1. **Observer** — detects violations (IP/subnet/ASN limits), publishes block events to RabbitMQ
2. **RabbitMQ** — message broker (block-event queue)
3. **Blocker** (Go service on each node) — consumes queue, adds IPs to nftables
4. **nftables** — kernel-level IP filtering

**Data Flow:**
```
User → Xray → Logs → Observer → RabbitMQ → Blocker → nftables (DROP packets)
```

### Problems with IP-Level Blocking

1. **Shared IPs / VPN rotation:**
   - Residential proxies use shared IP pools → blocking one IP affects many users
   - VPN services rotate IPs → abusers switch IPs instantly, bypassing blocks
   - Cloud providers (AWS, Azure) have shared IP ranges → collateral damage

2. **Operational complexity:**
   - 3-component architecture (Observer + RabbitMQ + Blocker on each node)
   - RabbitMQ requires clustering for HA, adds latency
   - Blocker deployment on each node increases maintenance burden
   - nftables rules can grow unbounded (memory issues)

3. **No automatic unblock:**
   - IPs stay blocked until TTL expires in Redis
   - If Blocker misses the unblock event (crash, network partition), IP stays blocked forever
   - Manual intervention required

4. **Limited effectiveness:**
   - Sophisticated abusers bypass IP blocks trivially (VPN, proxy chains)
   - Legitimate users on shared IPs get blocked (false positives)

---

## Decision

**Replace IP-level blocking with user-level enforcement via Remnawave Disable/Enable API.**

### New Architecture

**Components:**
1. **Observer** — detects violations, calls Remnawave API to disable user
2. **Remnawave Panel** — central user management, disables/enables users via API
3. **Vector Agents** (on nodes) — lightweight log collectors, no enforcement logic
4. **Redis** — disable scheduling for auto-enable

**Data Flow:**
```
User → Xray → Logs → Vector → Observer → Remnawave API (Disable User)
                                    ↓
                                 Redis (Schedule auto-enable)
                                    ↓
                              Scheduler → Remnawave API (Enable User)
```

### Key Changes

1. **User-level enforcement:**
   - Disable user account in Remnawave panel (revokes access keys)
   - Affects user across **all nodes** (no per-node state)
   - Bypassing requires new account (harder than changing IP)

2. **Simplified architecture:**
   - Removed: RabbitMQ, Blocker, nftables
   - Added: Vector (lightweight, <10 MB RAM)
   - Reduced latency: no message broker hop

3. **Automatic re-enable:**
   - Redis ZSET stores `(internal_id, until_unix)` pairs
   - Scheduler loop (`REENABLE_TICK_SECONDS=10s`) pops due disables
   - Calls Remnawave `EnableUser(uuid)` → automatic unblock

4. **Idempotent enforcement:**
   - Check existing disable record before calling API
   - If already disabled with `>= duration`, skip
   - Prevents duplicate API calls, reduces load

---

## Consequences

### Positive

1. **Higher effectiveness:**
   - Abusers can't bypass by changing IP
   - Requires new account + payment → higher friction

2. **Lower operational cost:**
   - No RabbitMQ cluster (saves CPU, memory, network)
   - No Blocker service (reduces deployment complexity)
   - Vector is self-contained, no dependencies

3. **No false positives:**
   - Shared IPs don't affect other users
   - Blocking is precise (exact user, not IP range)

4. **Global enforcement:**
   - User disabled across all nodes instantly
   - No per-node state synchronization

5. **Automatic recovery:**
   - Scheduler ensures users are re-enabled on time
   - Handles failures with exponential backoff + retry

6. **Better observability:**
   - Centralized enforcement metrics (`rw_disable_ok`, `rw_enable_fail`)
   - Trace full lifecycle: detection → disable → schedule → enable

### Negative

1. **Dependency on Remnawave API:**
   - If Remnawave is down, enforcement fails
   - Mitigation: Redis caches UUID mapping (24h TTL), temporary failures handled gracefully

2. **Latency:**
   - API call adds ~50-200ms vs local nftables (instant)
   - Acceptable: enforcement happens **after** violation detected, not real-time blocking

3. **Migration complexity:**
   - 10-commit migration (MIG-1 → MIG-10)
   - Requires careful rollout to avoid breaking production

4. **Breaking change:**
   - `user_email` field must now contain numeric ID (int64), not email
   - Vector configs on nodes must extract correct field from logs

### Neutral

1. **Vector deployment:**
   - Requires Docker on nodes (most already have it)
   - Configuration per node (but simple: ~50 lines TOML)

2. **Redis dependency:**
   - Already used for IP/subnet tracking
   - Added keys: `rw:uid2uuid:{id}`, `rw:disable:{id}`, `rw:reenable:zset`

---

## Implementation Timeline

| Commit | What | Status |
|---|---|---|
| MIG-1 | Migration plan | ✅ Done |
| MIG-2 | Enforcement package skeleton | ✅ Done |
| MIG-3 | Remnawave client + tests | ✅ Done |
| MIG-4 | Redis disable-schedule keys | ✅ Done |
| MIG-5 | RemnawaveEnforcer implementation | ✅ Done |
| MIG-6 | Scheduler loop in main.go | ✅ Done |
| MIG-7 | Processor: replace publishBlockEvent | ✅ Done |
| MIG-8 | API server: remove publisher | ✅ Done |
| MIG-9 | Delete RabbitMQ package (882 lines removed) | ✅ Done |
| MIG-10 | Delete Blocker + Vector examples | 🚧 In Progress |

---

## Technical Details

### Enforcement Flow

```go
// 1. Detect violation
if res.StatusCode == 1 { // limit exceeded
    internalID, _ := parseInternalID(entry.UserEmail)
    duration, _ := parseBlockDuration(cfg.BlockDuration)
    reason := fmt.Sprintf("ip_limit_exceeded: %d/%d IPs", res.CurrentCount, maxIPs)

    // 2. Enforce (user-level)
    enforcer.DisableTempByInternalID(ctx, internalID, duration, reason, score)
    // → Calls Remnawave DisableUser(uuid)
    // → Schedules auto-enable in Redis ZSET
}
```

### Scheduler Loop

```go
// Every 10s (configurable)
nowUnix := time.Now().Unix()
ids := storage.PopDueDisables(ctx, nowUnix, 100) // batch=100

for _, id := range ids {
    rec := storage.GetDisableRecord(ctx, id)
    client.EnableUser(ctx, rec.UUID)
    storage.ClearDisableRecord(ctx, id)
}
```

### Redis Keys

```
rw:uid2uuid:{internalID} → "uuid-string" (TTL: 24h)
rw:disable:{internalID}  → JSON {uuid, untilUnix, reason, score} (TTL: until + 24h)
rw:reenable:zset         → ZSET {member=internalID, score=untilUnix}
```

---

## Rollback Plan

If user-level enforcement proves ineffective:

1. **Phase 1: Restore RabbitMQ + publisher**
   - Revert MIG-7, MIG-8, MIG-9 (all commits are independently revertible)
   - Re-deploy RabbitMQ cluster

2. **Phase 2: Re-deploy Blocker**
   - Restore `blocker/` directory from git history
   - Deploy to all nodes via Docker Compose

3. **Phase 3: Switch routing**
   - Configure Observer to publish block events (not disable users)
   - Stop Remnawave Enforcer + Scheduler

**Estimated rollback time:** 2-4 hours (with prepared rollback branch)

---

## Monitoring & Alerts

### Key Metrics

From `metrics/counters.go`:
```
rw_disable_ok         # Successful user disables
rw_disable_fail       # Failed disables (Remnawave API errors)
rw_enable_ok          # Successful auto-enables
rw_enable_fail        # Failed enables (requires manual intervention)
uuid_cache_hit        # UUID cache efficiency
uuid_cache_miss       # API calls for UUID resolution
```

### Alerts

1. **High disable failure rate:**
   ```
   rw_disable_fail / (rw_disable_ok + rw_disable_fail) > 0.05  # >5% failures
   → Check Remnawave API health
   ```

2. **Enable failures:**
   ```
   rw_enable_fail > 0  # Any failures
   → Manual intervention required, users stuck in disabled state
   ```

3. **UUID cache miss rate:**
   ```
   uuid_cache_miss / (uuid_cache_hit + uuid_cache_miss) > 0.2  # >20% misses
   → Increase cache TTL or check cache eviction
   ```

---

## References

- [Cleanup Plan](../cleanup-plan.md) — Full migration roadmap
- [Vector Agent Setup](../VECTOR-AGENT-SETUP.md) — Node deployment guide
- [Remnawave Client](../../internal/services/remnawave/client.go) — API integration
- [Scheduler Implementation](../../internal/services/enforcement/scheduler.go) — Auto-enable logic

---

**Decision rationale:** User-level enforcement is more effective, simpler to operate, and scales better than IP-level blocking. The migration complexity is justified by long-term maintainability gains.
