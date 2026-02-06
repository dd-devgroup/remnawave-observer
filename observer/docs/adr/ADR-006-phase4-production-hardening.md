# ADR 006 — Phase 4 Production Hardening: HTTP Timeouts, Mandatory Publish, SeqNo Confirms, Strict JSON

## Status

Accepted (Commits M, N, O, P)

## Context

After Phase 1–3 (PR1–PR8 + Commits A–L), Observer had solid RabbitMQ confirms, request limits, context timeouts, and SCAN budgets. However several production risks remained:

1. **HTTP server had no timeouts** — vulnerable to slowloris and connection exhaustion attacks.
2. **Silent message loss on misconfiguration** — if exchange/route didn't exist, `PublishWithDeferredConfirm` succeeded but message vanished (no binding).
3. **Goroutine leak in confirms** — `waitWithTimeout` spawned a goroutine per publish that lingered until broker ack (bounded but leaky).
4. **JSON backward compatibility** — no way to enable/disable `DisallowUnknownFields` for clients (Vector) that send extra metadata fields.

## Decisions

### Commit M: HTTP Server Timeouts

Added configurable timeouts to `http.Server` to prevent slowloris and connection exhaustion:

- `HTTP_READ_HEADER_TIMEOUT_SECONDS` (default: 5s)
- `HTTP_READ_TIMEOUT_SECONDS` (default: 15s)
- `HTTP_WRITE_TIMEOUT_SECONDS` (default: 15s)
- `HTTP_IDLE_TIMEOUT_SECONDS` (default: 60s)
- `HTTP_MAX_HEADER_BYTES` (default: 1MB)

Values ≤0 are clamped to defaults in `config.New()`. Graceful shutdown preserved.

### Commit N: Mandatory Publish + Returns Handling

Enabled `mandatory=true` on all publishes to detect unroutable messages:

- `channelWithReturns` wraps channel + `NotifyReturn` listener.
- `generateMessageID()` (crypto/rand 16 bytes hex) tracks each publish.
- 500ms check on `returns` channel before confirm wait.
- If return received → log + retry with backoff.

Prevents silent message loss when exchange has no bindings.

### Commit O: SeqNo-Based Confirms (No Per-Publish Goroutine)

Replaced `PublishWithDeferredConfirm` + `waitWithTimeout` with leak-free architecture:

- **confirmRouter goroutine** (1 per channel) reads `NotifyPublish` events.
- **confirmWaiters map[seqNo]*waiter** tracks pending confirms by sequence number.
- `GetNextPublishSeqNo()` before `Publish()` to get seqNo.
- `registerWaiter()` creates waiter with `time.AfterFunc` timeout (no goroutine).
- Timeout deletes waiter from map + sends `false` to result channel.
- On reconnect/channel close: `closeRouter()` closes all waiter channels → retry.

No goroutine-per-publish, no deadlock, bounded map size.

### Commit P: Strict JSON Decode Flag

Added `STRICT_JSON_DECODE` (default: false) for backward compatibility:

- **false** (default): extra fields ignored (compatible with Vector, other clients).
- **true**: `DisallowUnknownFields()` enabled → unknown field returns `400 invalid_json`.

Preserves existing behavior by default; opt-in for strict validation.

## Consequences

### Positive

- **HTTP timeouts** protect against slowloris, keep-alive abuse, header bombs.
- **Mandatory publish** catches routing misconfigurations immediately (no silent loss).
- **SeqNo confirms** eliminate goroutine leak, reduce memory pressure, maintain correctness.
- **Strict JSON flag** allows gradual migration to stricter validation without breaking existing clients.

### Trade-offs

- **HTTP timeouts**: conservative defaults may disconnect slow clients on poor networks (tunable via env).
- **Mandatory publish**: adds ~200ms latency per publish (return check timeout) when routing is correct. Can reduce to 100ms if needed.
- **SeqNo confirms**: more complex concurrency (map+mutex+timer), but thoroughly tested (4 unit tests + integration).
- **Strict JSON**: `DisallowUnknownFields` gives generic JSON error, not specific "unknown_field" code (Go stdlib limitation).

## Env Vars

| Variable | Default | Description |
|---|---|---|
| **HTTP Timeouts** |||
| `HTTP_READ_HEADER_TIMEOUT_SECONDS` | 5 | Timeout for reading request headers |
| `HTTP_READ_TIMEOUT_SECONDS` | 15 | Timeout for reading entire request |
| `HTTP_WRITE_TIMEOUT_SECONDS` | 15 | Timeout for writing response |
| `HTTP_IDLE_TIMEOUT_SECONDS` | 60 | Timeout for keep-alive connections |
| `HTTP_MAX_HEADER_BYTES` | 1048576 | Max header size (1MB) |
| **JSON** |||
| `STRICT_JSON_DECODE` | false | Enable `DisallowUnknownFields` |
| **RabbitMQ (from Phase 2)** |||
| `PUBLISH_CONFIRM_TIMEOUT_MS` | 3000 | Max wait for broker confirm (now per-seqNo) |

## Integration Tests

Commit Q added real integration tests (`//go:build integration`):

- `docker-compose.test.yml` (Redis 7 + RabbitMQ 3.13)
- `Makefile` targets: `make test-integration`
- `storage_integration_test.go`: Redis SCAN with time budget (partial result check)
- `publisher_integration_test.go`: real publish + mandatory-without-binding test

Tests skip if `REDIS_URL`/`RABBITMQ_URL` not set (CI-friendly).

## Rollback Strategy

Each commit is independently revertible:

- **M**: `git revert 25fe607` — removes HTTP timeouts (back to default Go behavior).
- **N**: `git revert 255e851` — disables mandatory publish (no return checks).
- **O**: `git revert f8ba532` — reverts to `waitWithTimeout` (goroutine-per-publish).
- **P**: `git revert d6f3ba5` — removes `STRICT_JSON_DECODE` flag (always lenient).
- **Q**: `git revert 6fce13f` — removes integration tests (unit tests remain).

No breaking changes to external APIs or message formats. All configs have safe defaults.
