# ADR-012: Drain departing servers via the configuration API (reload-safe)

## Status
**Accepted — not yet implemented.** Amends the drain mechanism introduced for
ADR-006 (graceful server drain). Until implemented, the workaround is per-service
`shutdown_delay` tuning (see webapp: nginx 30s, php 35s).

## Context

ADR-006 changed service deregistration from *immediate deletion* to a graceful
**drain → wait `drain_timeout` (10s) → delete**, to fix 503s caused by immediate
deletion (in-flight connections cut, and a "no healthy server" gap before the
replacement alloc is healthy).

The drain is implemented with the DataPlane **runtime** API:

```
DrainServer → SetServerState(..., "drain") → PUT /v3/services/haproxy/runtime/backends/<b>/servers/<s>
```

The DataPlane API has two namespaces with different persistence:
- **`/configuration/...`** — edits the config file; persistent; requires a reload to apply.
- **`/runtime/...`** — runtime socket; immediate; **wiped by the next reload.**

`CreateServer`/`DeleteServer` use `/configuration/` (persistent). The drain uses
`/runtime/` (ephemeral). So during a deploy — which triggers many reloads (server
add/remove, `apply-frontend-rules`, other services churning) — **a reload inside
the 10s drain window resurrects the draining server as active**: the new worker
reads the config, where that server still exists with no drain flag, and routes
new requests to an alloc that is mid-shutdown:
- php-fpm already gone → nginx returns **502** (observed 2026-06-11 15:25, webapp), or
- nginx already gone → connection refused → **503** (observed on earlier deploys).

Today's mitigation is `shutdown_delay` tuning (keep nginx+php-fpm fully alive long
enough that a mis-routed request still gets a 200). That is belt-and-suspenders,
not a fix, and it slows deploys.

Note: ADR-006's two original reasons for draining are now largely covered by other
mechanisms — the "no healthy server" gap by **canary ordering** (the new alloc
registers and is healthy before the old one deregisters), and in-flight completion
by HAProxy **seamless reload** + `shutdown_delay`. So the runtime drain has gone
from net-positive to net-negative: its only remaining effect is the resurrection bug.

## Decision

Make the drain **persistent / reload-safe** by expressing it through the
**configuration** API instead of the runtime API, so it survives reloads:

- On deregistration, set the server out of rotation via a *config* change — i.e.
  `weight 0` (graceful: no new connections, existing complete) or `disabled`
  (hard maint) — then `DeleteServer` after `drain_timeout`.
- HAProxy can't express "drain" as a static server keyword, so **`weight 0` is the
  graceful equivalent** (load balancer sends no new requests, existing sessions
  finish); `disabled`/maintenance is the non-graceful fallback.
- Keep the existing fallback to immediate deletion if the drain call fails.

This preserves ADR-006's guarantees for every service (canary or not) and removes
the resurrection bug at the source.

## Consequences

### Positive
- Fixes the deploy 502/503 at the source, for **all** services, independent of
  canary deployment or `shutdown_delay`.
- Lets `shutdown_delay` values drop back to modest amounts (faster deploys); they
  become a safety net, not the load-bearing fix.
- One authoritative, reload-safe source of truth for which servers are in rotation.

### Negative
- A config-API drain requires a reload to take effect (the runtime drain was
  immediate), so the drain costs one extra reload. Minor — reloads already happen
  throughout a deploy.

### Alternative considered: immediate delete (no drain)
Now that canary ordering + seamless reload + `shutdown_delay` cover ADR-006's two
failure modes, deleting the server immediately via the config API would also work.
**Rejected as the default** because it depends on every service being
canary-deployed and having `shutdown_delay`; the persistent-drain approach is
correct even without those.

## Implementation notes
- Replace `SetServerState(ctx, b, s, "drain")` (runtime) with a configuration-API
  server update (`weight 0` or `disabled`) carrying a config version, in
  `drainAndRemoveServer`; keep `scheduleDelayedServerRemoval` for the delete.
- Verify the connector backends use an algorithm where `weight 0` means "no new
  connections" (roundrobin/leastconn do).
- TDD: add tests asserting the drained server is expressed in the *configuration*
  (survives a simulated reload), not just runtime state.
- `just build-connector` + `just deploy`, then confirm a webapp deploy is clean at
  the log level (0 new 502/503), and reduce webapp `shutdown_delay`s afterward.

## References
- lb1 ADR-006 (graceful shutdown), connector commit `884c895`
- 2026-06-11 15:25 "YAY Webapp 502" deploy incident
- webapp `shutdown_delay` commits (nginx 30s, php 35s) — the current workaround
