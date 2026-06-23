# ADR-009: Frontend Rule Missing When Backend Pre-exists

**Date:** 2025-08-16  
**Status:** Identified  
**Tags:** bug, frontend-rules, service-registration

## Problem

When a HAProxy backend already exists and a new service registers with the connector, the frontend rule creation may be skipped, leaving the service unreachable despite being healthy.

## Context

During CRM service deployment, we observed:
- Service registered successfully with Nomad
- HAProxy backend `crm_prod` existed and server was added
- Health checks passed and server showed as UP
- Service remained unreachable with 503 errors
- Frontend rule for `crm.ps-webforge.net -> crm_prod` was missing

## Root Cause Analysis

The connector appears to have logic that assumes:
> "If backend exists, frontend rule must also exist"

This leads to a bug where:
1. Backend exists (from previous deployment or static config)
2. New service registration occurs
3. Connector adds server to existing backend ✅
4. Connector skips frontend rule creation ❌
5. Service becomes unreachable despite being healthy

## Evidence

**Before connector restart:**
```bash
# Backend existed and was healthy
ssh lb1 'echo "@1 show servers state crm_prod" | socat stdio /run/haproxy-master.sock'
# Output: crm_prod server UP and healthy

# Frontend rule was missing
just debug-frontend-rules | grep crm
# Output: (empty - no CRM rule)

# Service unreachable
curl -I https://crm.ps-webforge.net
# Output: HTTP/1.1 503 Service Unavailable
```

**After connector restart (forced reconciliation):**
```bash
# Frontend rule created
just debug-frontend-rules | grep crm
# Output: {"cond": "if", "cond_test": "is_crm_prod_97478491", "name": "crm_prod"}

# Service reachable
curl -I https://crm.ps-webforge.net  
# Output: HTTP/1.1 302 Found (normal application response)
```

## Impact

- **Service Outages:** Services become unreachable despite being healthy
- **Silent Failures:** No obvious errors in logs, appears as mysterious 503s
- **Manual Intervention Required:** Requires connector restart to fix
- **Deployment Risk:** Can affect production services during migrations

## Recommended Fix

The connector should always verify both backend AND frontend rule existence for each service registration, regardless of pre-existing state.

**Current logic (problematic):**
```
if backend_exists {
    add_server_to_backend()
    // ❌ Assumes frontend rule exists
} else {
    create_backend()
    create_frontend_rule() 
    add_server_to_backend()
}
```

**Proposed logic:**
```
if !backend_exists {
    create_backend()
}
add_server_to_backend()

if !frontend_rule_exists {
    create_frontend_rule()  // ✅ Always check independently
}
```

## Additional Evidence (2025-08-16 09:08)

**NEW BUG DISCOVERED:** The issue also occurs during canary deployments when old servers are deregistered:

```
09:06:41 - CRM service registered successfully, frontend rule existed  
09:08:17 - CRM service deregistered and frontend rule was REMOVED
```

Connector log:
```
Successfully processed ServiceDeregistration for service crm-prod (status=deleted, frontend_rule_removed=crm.ps-webforge.net, backend=crm_prod, server=crm_prod_192_168_5_12_29785)
```

**Root cause:** Connector removes frontend rules when ANY server is deregistered, even if other healthy servers for the same service are still running.

## Workaround

Until fixed, restart the connector after service deployments if 503 errors occur:
```bash
ssh lb1 'systemctl restart haproxy-nomad-connector'
```

## Related

- Initial discovery during photobooks-web zero-downtime deployment investigation
- Affects services migrating from static to dynamic port configuration
- May impact other services using pre-existing backends

## Decision

This ADR documents the bug for tracking and resolution. The connector should be updated to decouple backend and frontend rule creation logic.