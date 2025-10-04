# INCIDENT-001: systemctl restart HAProxy vs DataPlane API State Conflicts

**Date:** 2025-09-02  
**Status:** Resolved  
**Severity:** High  
**Duration:** Multiple occurrences, ~15 minutes total downtime  
**Services Affected:** All HAProxy-routed services (yayphotobooks.com, crm.ps-webforge.net, etc.)

## Summary

Multiple service outages occurred due to fundamental incompatibility between `systemctl restart haproxy` and DataPlane API dynamic configuration. The root cause was using systemctl commands that reset HAProxy to base configuration, destroying all dynamically created backends, servers, and frontend rules managed by the haproxy-nomad-connector.

## Timeline

**17:25:16** - HAProxy restarted with correct config paths  
**17:25:49** - Connector restarted  
**17:25:50-51** - All services failed with validation errors  
**17:29:48** - Second connector restart required  
**17:29:49** - Services gradually restored  
**18:01:00** - Deployment attempt with systemd override  
**18:02:16** - Service outage due to broken override logic  
**18:03:26** - Manual recovery and service restoration  

## Root Cause Analysis

### Primary Issue: systemctl vs DataPlane API Incompatibility

**The fundamental problem:**
1. **HAProxy base config** contains only static configuration (frontends, basic backends)
2. **DataPlane API** adds dynamic configuration (connector-managed backends, servers, rules)
3. **systemctl restart** reloads HAProxy with ONLY the base config
4. **All dynamic configuration is lost** (backends, servers, frontend rules)
5. **Connector expects dynamic objects to exist** but they're gone

### Technical Details

**What gets lost on systemctl restart:**
- Dynamic backends created by connector (e.g., `photobooks_web`, `crm_prod`)
- Dynamic servers within those backends  
- Frontend switching rules for domain routing
- All DataPlane API-managed configuration

**Connector impact:**
- Fails to set servers to ready state (404 errors - backend doesn't exist)
- Frontend rules not recreated on initial sync
- Services return 503 until connector restart recreates everything

### Secondary Issue: Complex Prevention Attempts

**Failed systemd override approach:**
- Attempted complex conditional overrides with bypass files
- Logic errors in systemd conditions (`ConditionPathExists`)
- Caused additional outages during "safe" deployment
- Override complexity made troubleshooting harder

## Impact Assessment

**Service Availability:**
- Multiple 5-15 minute outages for all HAProxy-routed services
- 503 errors for end users during connector recovery
- Required manual intervention for each incident

**Operational Impact:**
- Developer time lost troubleshooting
- Multiple restart cycles to restore service
- Documentation and process failures

## Resolution

### Immediate Actions Taken
1. **Removed broken systemd overrides** - restored normal HAProxy operation
2. **Restarted connector** - recreated all dynamic configuration  
3. **Verified service restoration** - all domains responding correctly

### Long-term Solution
1. **Enhanced documentation** - updated CLAUDE.md with clearer warnings
2. **Added prevention mechanism** - `just restart-haproxy` command that fails with explanation
3. **Simplified approach** - abandoned complex systemd overrides

## Lessons Learned

### Technical Lessons

1. **systemctl restart and DataPlane API are fundamentally incompatible**
   - systemctl operations reset to base configuration
   - DataPlane API state is not persisted across restarts
   - Any restart destroys dynamic configuration

2. **Connector initial sync has bugs**
   - Inconsistent frontend rule creation compared to event stream
   - Some services processed correctly, others not
   - Mock testing hid real API interaction issues

3. **Complex prevention mechanisms can cause more problems**
   - systemd overrides are fragile and hard to debug
   - Conditional logic in system services is error-prone
   - Simple solutions are more reliable

### Process Lessons

1. **Documentation warnings are insufficient**
   - Clear warnings in CLAUDE.md were repeatedly ignored
   - People instinctively reach for systemctl restart when troubleshooting
   - Need technical prevention, not just documentation

2. **"Safe" deployment assumptions were wrong**
   - Assumed systemd overrides would be backward compatible
   - Didn't test deployment logic before applying
   - Safe deployment caused the exact problem we were trying to prevent

3. **Understanding system behavior is critical**
   - Misunderstood systemd override conditions
   - Deployed changes without verifying behavior
   - Testing in production is dangerous

## Prevention Measures

### Implemented Solutions

1. **Simple prevention command:**
   ```bash
   just restart-haproxy  # Fails with clear error message
   ```

2. **Clear guidance:**
   - Explains WHY restart is dangerous
   - Points to correct solution: `just reload`
   - Preserves emergency capability

3. **Clean operational procedures:**
   - Normal operations: `just reload` (DataPlane API)
   - Emergency situations: Direct systemctl still possible
   - Boot recovery: HAProxy starts normally

### Recommendations

1. **Fix connector initial sync bugs**
   - Ensure frontend rules created consistently
   - Test against real HAProxy, not mocks
   - Improve service restoration reliability

2. **Consider DataPlane API persistence**
   - Investigate if dynamic config can be persisted
   - Explore backup/restore mechanisms
   - Document recovery procedures

3. **Improve testing practices**
   - Test all changes in staging first
   - Verify system behavior before production deployment
   - Never assume "safe" without testing

## Success Metrics

**Immediate Recovery:**
- ✅ All services restored and responding normally
- ✅ Connector operating correctly
- ✅ No remaining configuration conflicts

**Prevention:**
- ✅ `just restart-haproxy` prevents accidental restarts
- ✅ Clear error messages guide correct behavior
- ✅ Emergency override still available when needed

**Documentation:**
- ✅ Incident documented for future reference
- ✅ Lessons learned captured
- ✅ Prevention measures implemented

## Key Takeaways

1. **systemctl restart haproxy should never be used** in environments with DataPlane API dynamic configuration
2. **Simple solutions are better than complex ones** for operational safety
3. **Test everything** - especially "safe" deployment procedures
4. **Documentation alone is insufficient** - need technical prevention
5. **Understand the tools** before implementing complex system modifications

This incident highlighted fundamental architectural incompatibilities and the importance of understanding system interactions before implementing safety measures.