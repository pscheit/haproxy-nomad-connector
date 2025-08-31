# ADR-010: HAProxy 3.0 Runtime API Requirement

**Date:** 2025-08-31  
**Status:** Resolved  
**Tags:** haproxy, runtime-api, production-outage, version-compatibility

## Problem

On 2025-08-31, production experienced a catastrophic 6-hour outage where all HAProxy backends showed as DOWN despite services being healthy in Nomad. The connector was successfully adding servers to backend configurations but failing to enable them in runtime.

### Symptoms
- All backends showing DOWN status despite healthy Nomad services
- Connector errors: `failed to set server X to ready state: API request failed with status 404: no data for backend/server: not found`
- Services unreachable with 503 errors
- Manual socket commands (`echo "@1 enable server..."`) worked as workaround

### Investigation Timeline
1. **Initial diagnosis**: Suspected connector logic bugs or service registration issues
2. **Fix attempt**: Added `ReadyServer()` calls after server creation (commit 5854d2f)
3. **Testing**: Mock tests passed, no real integration tests for runtime API
4. **Production failure**: Fix failed completely in production
5. **Root cause discovery**: HAProxy 2.6 vs 3.0 runtime API compatibility difference

## Root Cause Analysis

### HAProxy Version Incompatibility

**Production Environment:**
- HAProxy 2.6.12 (Debian 12 standard package)
- DataPlane API runtime endpoints return 404 for dynamically added servers
- Runtime server state management broken for configuration-added servers

**Development Environment:**
- HAProxy 3.0.11 (Docker container)
- DataPlane API runtime endpoints work correctly
- Full runtime server state management support

### Technical Details

The DataPlane API `/v3/services/haproxy/runtime/backends/{backend}/servers/{server}` endpoint:
- **HAProxy 2.6**: Returns 404 for servers added via configuration API
- **HAProxy 3.0**: Works correctly for all servers (static + dynamic)

This means the connector's approach of:
1. Add server via configuration API (`CreateServer`)
2. Enable server via runtime API (`ReadyServer`)

Only works on HAProxy 3.0+.

### Testing Gap

The critical testing gap was relying on mock tests instead of real HAProxy integration:

**Mock Test (passed incorrectly):**
```go
func (m *mockHAProxyClient) ReadyServer(backendName, serverName string) error {
    return nil  // Always succeeds, hiding real API issues
}
```

**Real HAProxy 2.6 (failed in production):**
```bash
curl -X PUT .../runtime/backends/test/servers/server1
# Returns: {"code":404,"message":"no data for test/server1: not found"}
```

**Real HAProxy 3.0 (works correctly):**
```bash
curl -X PUT .../runtime/backends/test/servers/server1
# Returns: {"admin_state":"ready","operational_state":"up",...}
```

## Decision

**Require HAProxy 3.0+ for haproxy-nomad-connector.**

### Rationale
1. **Runtime API compatibility**: Only 3.0+ has reliable runtime server management
2. **Production stability**: Eliminates the root cause of service outages
3. **Future-proofing**: HAProxy 3.0 is LTS until Q2 2029
4. **Simplified architecture**: No need for workarounds or fallback mechanisms

## Implementation

### Immediate Actions (Completed)
1. ✅ Added HAProxy 3.0 repository to Ansible deployment
2. ✅ Updated production servers to HAProxy 3.0.11
3. ✅ Verified all backends operational after upgrade
4. ✅ Confirmed connector ReadyServer() calls now work correctly

### Documentation Updates (This ADR)
1. Update README.md with HAProxy 3.0+ requirement
2. Add version compatibility section to documentation
3. Update installation instructions for HAProxy 3.0
4. Document testing requirements (no more mock-only tests)

### Long-term Improvements
1. **Integration tests**: Must test against real HAProxy, not mocks
2. **Version detection**: Consider adding HAProxy version checks to connector
3. **CI/CD**: Test against multiple HAProxy versions in CI pipeline

## Consequences

### Positive
- **Production stability**: Eliminates the runtime API compatibility issues
- **Simplified codebase**: No need for version-specific workarounds
- **Better testing**: Forces real integration testing instead of mocks
- **Long-term support**: HAProxy 3.0 LTS until Q2 2029

### Negative
- **Deployment requirement**: Requires HAProxy repository setup (not just Debian packages)
- **Breaking change**: Existing HAProxy 2.x deployments need upgrade
- **Documentation debt**: Previous examples and docs may reference 2.x

### Risks and Mitigations
- **Risk**: HAProxy 3.0 introduces behavioral changes
  - **Mitigation**: Thorough testing after upgrade, monitor metrics
- **Risk**: Repository availability issues
  - **Mitigation**: Use official HAProxy Debian repository, document fallback
- **Risk**: Performance differences between versions
  - **Mitigation**: Performance testing, gradual rollout

## Testing Lessons Learned

### Critical Testing Gaps
1. **Mock-only runtime API testing**: Hid fundamental compatibility issues
2. **No version matrix testing**: Never tested connector against HAProxy 2.x
3. **Configuration-focused tests**: Tested backend creation but not runtime state

### New Testing Requirements
1. **Real HAProxy integration tests**: All runtime API functionality
2. **Version compatibility testing**: Test against supported HAProxy versions
3. **End-to-end scenarios**: Full service registration → ready state workflow
4. **Production-like environments**: Use same HAProxy version as production

## References

- **Production Outage**: 2025-08-31, 6-hour service disruption
- **Failed Fix**: Commit 5854d2f - HAProxy 2.x server health check bug fix
- **HAProxy Versions**: 2.6.12 (broken) vs 3.0.11 (working)
- **DataPlane API**: Runtime endpoints `/v3/services/haproxy/runtime/backends/{backend}/servers/{server}`

## Success Metrics

- ✅ Zero service outages due to runtime API issues since HAProxy 3.0 upgrade
- ✅ All connector ReadyServer() calls succeed in production
- ✅ Backend health status accurately reflects service state
- 🎯 Target: <1 minute recovery time for service registration events

This ADR documents a critical production stability fix and establishes HAProxy 3.0 as the minimum supported version for reliable operation.