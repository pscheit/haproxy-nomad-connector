# ADR-013: Frontend rules are ordered by match specificity

## Status
**Accepted — implemented.** Extends ADR-008 (dynamic frontend rule management).

## Context

HAProxy evaluates `use_backend` rules top to bottom and takes the **first** match.
The connector wrote ACLs and backend switching rules in **registration order**:
`AddFrontendRuleWithType` appended each new domain to the end of the list.

So the backend that serves a host depended on which service registered first — and
that order could flip on any connector restart or resync.

On 2026-08-30 this made `pictor.yaymemories.com` unreachable. Live config on lb1:

```
position 6:  use_backend webapp_prod  if is_webapp_prod_69a242d1   # hdr(host) -m reg ^[-a-zA-Z0-9._]+\.yaymemories\.com$
position 14: use_backend pictor_prod  if is_pictor_prod_109776fd   # hdr(host) pictor.yaymemories.com
```

webapp's catch-all regex for `*.yaymemories.com` matched `pictor.yaymemories.com`
first, so the exact rule at position 14 was dead and `/healthcheck` returned webapp's
404. Every future exact `*.yaymemories.com` subdomain had the same trap waiting.

## Decision

The connector orders frontend rules by **match specificity**: exact → prefix → regex.
Rules of the same type keep their relative order, so overlapping regexes still resolve
by registration order.

Two places enforce it:

1. **On every write.** `setFrontendRulesInTransaction` sorts before it emits the ACLs
   and switching rules. Both writers (`AddFrontendRuleWithType`, `RemoveFrontendRule`)
   pass through it, so the invariant cannot be lost by adding or removing a domain.

2. **On startup.** `ReorderFrontendRules` runs at the end of `syncExistingServices`.
   This is needed because `reconcileFrontendRule` skips the write when a rule for the
   domain+backend pair already exists — without an explicit reorder, a restart would
   never rewrite the list and an existing bad order would survive forever.
   `ReorderFrontendRules` opens no transaction when the order is already correct, so
   it normally costs no config version bump and no reload.

## Consequences

### Positive
- A catch-all regex can never shadow an exact host rule.
- Routing no longer depends on service registration order.
- Existing bad order self-heals on the next connector start — no manual DataPlane API
  fix, which ADR-005 forbids anyway.

### Negative / limits
- Two overlapping regexes still resolve by registration order. If that ever matters,
  the rules need an explicit priority, not a heuristic.
- The first start after this change rewrites the rule list once, which costs one
  reload. Seamless reload, so no dropped connections.

### Out of scope
`DomainTypePrefix` is emitted as a plain `hdr(host)` value, which HAProxy evaluates as
an exact match — prefix matching does not actually work. No service uses
`haproxy.domain.type=prefix`. It ranks between exact and regex; the behavior is
unchanged.
