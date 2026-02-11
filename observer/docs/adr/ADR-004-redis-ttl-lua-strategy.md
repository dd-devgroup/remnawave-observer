# ADR 004 — Redis set TTL guard in Lua scripts (+ SCAN key budget)

## Status

Accepted (PR5, PR7)

## Context

### TTL reset (PR5)

`add_and_check_ip.lua` and `add_and_check_subnet.lua` both maintain a Redis
SET of per-user identifiers and call `EXPIRE key TTL` unconditionally on every
execution.  When a new member is added to an **existing** set the EXPIRE call
resets the TTL to the full window.  This means a set that should have expired
after 24 h stays alive indefinitely as long as traffic keeps arriving — the
absolute deadline is never enforced.

### SCAN unbounded iteration (PR7)

`GetAllUserEmails` and `GetAllIPsForUser` iterate Redis keyspace with SCAN /
Iterator without any upper bound.  On a keyspace with millions of keys the
monitor loop could hold a connection open for minutes, blocking other clients
and consuming memory for the accumulated result slice.

## Decision

### TTL guard

Before calling EXPIRE the Lua scripts now check the current TTL:

```lua
local currentSetTTL = redis.call('TTL', KEYS[1])
if currentSetTTL < 0 then
    redis.call('EXPIRE', KEYS[1], ARGV[2])
end
```

`TTL` returns `-2` (key does not exist) or `-1` (key exists but has no
expiry).  Both are `< 0`, so the EXPIRE fires only on first creation or after
an unexpected TTL loss.  Once a TTL is set it is never touched again by the
script — the set expires at its original absolute deadline regardless of
subsequent member additions.

The guard is applied identically in both `add_and_check_ip.lua` and
`add_and_check_subnet.lua`.  No change to the Lua SHA loading mechanism is
needed: `ScriptLoad` is called at startup and the SHA updates automatically
when the script body changes.

### SCAN key budget

A `scanMaxKeys` field (default 10 000, env `SCAN_MAX_KEYS`) is added to
`RedisStore`.

* `GetAllUserEmails` — a shared `scanned` counter increments by
  `len(keys)` after every `SCAN` batch.  The inner cursor loop breaks when
  `cursor == 0 || scanned >= scanMaxKeys`; the outer pattern loop also breaks
  and logs a warning when the limit is reached.
* `GetAllIPsForUser` — the `Iterator` loop increments a counter per key and
  breaks when it exceeds `scanMaxKeys`.

Both functions return whatever results were collected up to the cutoff.  The
monitor treats this as a best-effort snapshot; over-limit users are still
detected on the next cycle.

## Consequences

- Per-user sets now have a hard, absolute TTL.  Long-lived sessions no longer
  accumulate indefinitely.
- The SCAN budget prevents runaway iterations on large keyspaces without
  requiring cursor-based pagination at the application level.
- The budget is tunable without code changes.
- Existing data is not affected: sets that already have a TTL keep it;
  sets without one get it on the next Lua invocation (TTL == -1 path).

## Env vars

| Variable | Default | Description |
|---|---|---|
| `SCAN_MAX_KEYS` | 10000 | Max keys scanned per GetAllUserEmails / GetAllIPsForUser call |
