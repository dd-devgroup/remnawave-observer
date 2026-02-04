# ADR 003 — Per-task context timeout on side-effect workers

## Status

Accepted (PR4)

## Context

Side-effect tasks (webhook alerts, delayed IP/subnet/ASN cleanup) are
dispatched through a `chan func(context.Context)` and executed by a dedicated
goroutine pool.  Before PR4 the closures captured the **request-level**
context from the outer `processSingleEntry` scope.  That context was already
done by the time the task ran (the HTTP request had finished), so every
downstream HTTP call and Redis operation inside the task saw an immediately
cancelled context — silently failing or, worse, blocking on a stale deadline.

Additionally, a single misbehaving task (e.g. a webhook that never responds)
could monopolise a worker goroutine indefinitely, starving the rest of the
side-effect queue.

## Decision

1. The closure signature was changed to `func(context.Context)` — each
   closure receives a **fresh** context, not the captured request context.

2. The worker loop wraps every task invocation in
   `context.WithTimeout(appCtx, cfg.SideEffectTimeout)` (default 10 s).
   `appCtx` is the application-level context (cancelled only on SIGTERM).

3. The timeout duration is configurable via `SIDE_EFFECT_TIMEOUT_SECONDS`.

4. All three `SendAlert` call sites inside the closures now forward the
   provided `ctx` to the alerter, which in turn uses it for
   `http.NewRequestWithContext`.

## Consequences

- Webhook alerts and cleanup operations use a live, bounded context.
- A stuck webhook times out after 10 s, freeing the worker for the next task.
- The application-level context is still honoured: on SIGTERM the `select`
  on `ctx.Done()` in the worker loop skips pending tasks gracefully.
- No change to the side-effect channel buffer or pool size semantics.

## Env vars

| Variable | Default | Description |
|---|---|---|
| `SIDE_EFFECT_TIMEOUT_SECONDS` | 10 | Max wall-clock time per side-effect task |
