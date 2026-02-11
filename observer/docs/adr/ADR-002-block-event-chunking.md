# ADR 002 — Block-event chunking and schema versioning

## Status

Accepted (PR8)

## Context

When a user violates the ASN or IP limit the processor collects every IP
associated with that user and publishes them in a single `BlockMessage`.  In
pathological cases (large ASN with hundreds of mapped IPs, or a very high
`MAX_IPS_PER_USER`) the list can exceed practical AMQP frame sizes or cause
the downstream Blocker to process an unexpectedly large batch in one
transaction.

## Decision

A `MaxIPsPerBlockEvent` threshold (default 500, env `MAX_IPS_PER_BLOCK_EVENT`)
is introduced.

* **Single chunk (len ≤ threshold)** — the message is published exactly as
  before: `{"ips":[…],"duration":"…"}`.  No new fields are emitted.  The
  Blocker does not need any code change for this path.

* **Multiple chunks (len > threshold)** — the list is sliced into ceil(N / 500)
  pieces.  Each piece is published as a separate message with an envelope:

  ```json
  {
    "ips": ["…"],
    "duration": "5m",
    "event_id": "<32-char hex, crypto/rand>",
    "chunk_index": 0,
    "chunk_total": 3,
    "schema_version": 2
  }
  ```

  `event_id` is a 16-byte value from `crypto/rand`, hex-encoded.  It ties all
  chunks of one logical block event together so the Blocker can reassemble or
  process them independently while still correlating in logs.

  `schema_version: 2` lets the consumer distinguish chunked messages from
  legacy single messages without inspecting optional fields.

* All new fields use `omitempty` in the Go struct so the single-chunk wire
  format is byte-identical to the previous schema.

## Consequences

- The Blocker can start processing chunks as they arrive without waiting for
  the full set; eventual consistency is acceptable for firewall rule updates.
- `event_id` enables end-to-end tracing of a block event across multiple AMQP
  messages.
- Backward compatibility is preserved: a Blocker that does not understand
  `schema_version` simply sees a normal `BlockMessage` for single-chunk events
  and processes each chunk independently for multi-chunk events (same IPs,
  same duration).
- The threshold is tunable without code changes.

## Env vars

| Variable | Default | Description |
|---|---|---|
| `MAX_IPS_PER_BLOCK_EVENT` | 500 | Max IPs per single AMQP message |
