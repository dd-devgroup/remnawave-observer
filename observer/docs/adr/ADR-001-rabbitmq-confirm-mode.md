# ADR 001 — RabbitMQ publish confirm mode + persistent delivery

## Status

Accepted (PR3)

## Context

Observer publishes block-event messages to a RabbitMQ fanout exchange consumed
by the Blocker component.  Before PR3 the publisher used a single channel with
no delivery guarantees: a broker restart or a momentary network blip silently
dropped messages, causing IPs to remain unblocked until the next violation was
detected.

Retries used a fixed `time.Sleep` with no jitter, causing thundering-herd
behaviour when multiple publishers recovered at the same time.

## Decision

1. **Channel pool** — `poolSize` channels are opened at startup (default 5).
   Each channel is independently put into confirm mode via `ch.Confirm(false)`.
   A buffered `chan *amqp091.Channel` acts as a lock-free semaphore; a worker
   grabs a channel, publishes, waits for the broker ack, then returns it.

2. **Deferred confirm** — `ch.PublishWithDeferredConfirm(...)` returns a
   `DeferredConfirm` whose `.Wait()` blocks until the broker acks or nacks.
   `true` = ack (message persisted or routed), `false` = nack (retry).

3. **Persistent delivery** — every `amqp091.Publishing` sets
   `DeliveryMode: amqp091.Persistent` so messages survive a broker restart.

4. **Full-jitter exponential backoff** — on each retry attempt the sleep is
   `rand(0, min(cap, base * 2^attempt))`.  `base` defaults to 500 ms, `cap`
   to 30 s.  The shift exponent is hard-capped at 30 to prevent int overflow.
   All four parameters are configurable via environment variables.

5. **Reconnect on closed connection** — before each publish attempt the code
   checks `conn.IsClosed()`; if true it calls `connectWithRetry` (same
   backoff) to rebuild the pool before retrying the publish.

## Consequences

- Every message that reaches the broker is durably stored; no silent drops.
- Thundering-herd on reconnect is eliminated by per-attempt jitter.
- The channel pool prevents goroutine contention without mutexes.
- Latency per publish increases by one round-trip (ack wait), which is
  acceptable given the low message rate (one per block event).

## Env vars

| Variable | Default | Description |
|---|---|---|
| `PUBLISHER_POOL_SIZE` | 5 | Number of confirm-mode channels |
| `RABBIT_PUBLISH_MAX_RETRIES` | 5 | Max publish attempts |
| `RABBIT_PUBLISH_BACKOFF_BASE_MS` | 500 | Backoff base (ms) |
| `RABBIT_PUBLISH_BACKOFF_MAX_MS` | 30000 | Backoff cap (ms) |
