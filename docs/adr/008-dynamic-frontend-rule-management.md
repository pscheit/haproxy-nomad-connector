# ADR-008: Dynamic Frontend Rule Management via DataPlane API

**Status:** Proposed  
**Date:** 2025-08-12  
**Authors:** Claude (via investigation)

## Context

The haproxy-nomad-connector currently has a hybrid approach for domain routing that creates operational overhead:

### Current State (Half-Manual)
1. **Dynamic backend management** ✅ - Connector creates backends (e.g., `photobooks_web`, `ps_webforge`)
2. **Dynamic server management** ✅ - Connector adds/removes servers via DataPlane API  
3. **Domain routing** ❌ - Requires manual maintenance of `/etc/haproxy2/domain-backend.map`

### The Operational Problem
With 12 services in Nomad + 1 deployed elsewhere:
- **13 manual map entries** to maintain
- **Deployment coupling** - Can't deploy new Nomad service without touching HAProxy config
- **Error-prone** - Forgetting map entries leads to 503 errors
- **Mixed deployment complexity** - Some services in Nomad, some elsewhere, all need manual routing

## Problem Statement

Services with `haproxy.domain=example.com` tags should automatically create frontend routing rules without requiring any manual configuration file maintenance.

## Decision

**Eliminate the domain-backend.map file entirely** and use DataPlane API to dynamically manage frontend ACL and use_backend rules.

## Proposed Architecture

### Dynamic Frontend Rules
The connector will manage frontend rules via DataPlane API:

```haproxy
frontend https
    # Dynamic ACLs (managed by connector) - FIRST PRIORITY
    acl is_yayphotobooks hdr(host) -i yayphotobooks.com
    use_backend photobooks_web if is_yayphotobooks
    
    acl is_ps_webforge hdr(host) -i ps-webforge.com  
    use_backend ps_webforge if is_ps_webforge
    
    # Manual fallback rules (if needed) - LAST PRIORITY
    # Can be manually added via DataPlane API for non-Nomad services
```

### Service Tag Processing
Services use existing `haproxy.domain` tags:
```hcl
service {
  name = "photobooks-web"
  tags = [
    "haproxy.enable=true",
    "haproxy.domain=yayphotobooks.com",     # Creates ACL + use_backend rule
    "haproxy.domain=www.yayphotobooks.com", # Multiple domains supported
  ]
}
```

### Rule Lifecycle
- **Service registration:** Add ACL + use_backend rule to frontend
- **Service deregistration:** Remove ACL + use_backend rule when backend has no servers
- **Rule ordering:** Dynamic rules get higher priority than manual rules

## Implementation Plan

### Phase 1: DataPlane API Frontend Management
1. **Research DataPlane API capabilities**
   - Investigate frontend ACL management endpoints
   - Determine if rule ordering can be controlled
   - Identify API patterns for ACL + use_backend rule pairs

2. **Extend HAProxy client**
   - Add `AddFrontendACL(frontend, acl, condition)` method
   - Add `AddFrontendRule(frontend, rule, condition)` method  
   - Add `RemoveFrontendACL()` and `RemoveFrontendRule()` methods
   - Handle rule ordering/priority if supported

### Phase 2: Connector Integration
1. **Frontend rule processing**
   - Extract domain tags from service events
   - Generate ACL names (e.g., `is_photobooks_web_yayphotobooks_com`)
   - Create ACL + use_backend rule pairs
   - Handle multiple domains per service

2. **Rule cleanup**
   - Track which rules belong to which services
   - Remove rules when last server is removed from backend
   - Prevent rule conflicts between services

### Phase 3: Configuration Cleanup
1. **Remove domain map configuration**
   - Remove `domain_map` section from config schema
   - Remove `DomainMapManager` initialization
   - Update documentation and examples

2. **Update HAProxy base config**
   - Remove `use_backend %[req.hdr(host),lower,map_dom(...)]` fallback
   - Or keep as final fallback for manually managed services

## Technical Challenges

### Rule Ordering
**Critical Issue:** DataPlane API must support rule ordering to ensure:
- Dynamic rules have higher priority than manual rules
- Multiple dynamic rules don't conflict
- Fallback behavior works correctly

### ACL Naming Convention
Generate consistent ACL names:
```
Pattern: is_{service_name}_{domain_sanitized}
Example: is_photobooks_web_yayphotobooks_com
```

### Rule Synchronization
Ensure frontend rules stay in sync with backend existence:
- Add rules when first server is added to backend
- Remove rules when last server is removed from backend
- Handle connector restarts (rule reconciliation)

## Investigation Results

**Completed:** 2025-08-12

✅ **DataPlane API Frontend Support**
- ACL management: `PUT /v3/services/haproxy/configuration/frontends/{name}/acls` (array replacement)
- Backend switching rules: `PUT /v3/services/haproxy/configuration/frontends/{name}/backend_switching_rules` (array replacement)
- Requires transaction-based updates for atomic changes
- No individual POST/DELETE - must replace entire arrays

✅ **Rule Ordering Control**
- Rules are applied in array order provided to API
- Full control over rule precedence through array positioning
- Dynamic rules can be placed before/after static rules as needed

✅ **Configuration Reload Behavior**
- DataPlane API handles HAProxy reloads automatically
- Changes are persisted to config file immediately
- Rules survive HAProxy restarts

**Key API Pattern:**
```bash
# Create transaction
POST /v3/services/haproxy/transactions?version=N

# Replace ACL list
PUT /frontends/https/acls?transaction_id=X
[{acl_name: "is_domain", criterion: "hdr(host)", value: "example.com"}]

# Replace backend switching rules  
PUT /frontends/https/backend_switching_rules?transaction_id=X
[{cond: "if", cond_test: "is_domain", name: "backend_name"}]

# Commit transaction
PUT /v3/services/haproxy/transactions/X
```

## Success Criteria

- [ ] Services with `haproxy.domain` tags automatically create frontend routing rules
- [ ] Multiple domains per service are supported
- [ ] Rules are removed when services are deregistered  
- [ ] Rule ordering ensures dynamic rules have priority
- [ ] No manual configuration file maintenance required
- [ ] Non-Nomad services can still be added manually via DataPlane API
- [ ] Zero-downtime rule updates during service deployments

## Backwards Compatibility

**Breaking Change:** This removes the domain-backend.map file approach entirely.

**Migration Path:**
1. Identify all manual domain mappings in current map file
2. Either add `haproxy.domain` tags to Nomad services
3. Or manually add ACL rules for non-Nomad services via DataPlane API

## Risk Assessment

**Medium Risk:** Depends heavily on DataPlane API capabilities that need investigation.

**Rollback Plan:** Revert to static domain-backend.map file if DataPlane API limitations prevent implementation.

## Alternative Approaches

If DataPlane API doesn't support adequate frontend rule management:
1. **Hybrid approach:** Keep map file for fallback, add dynamic rules for higher priority
2. **Template generation:** Generate HAProxy config sections and trigger reloads
3. **External tool:** Use separate service for frontend rule management

---

This ADR eliminates the "half-baked" hybrid approach and provides fully automated domain routing for Nomad services while maintaining flexibility for non-Nomad services.