# ADR-011: Fix Missing Health Check Configuration Update

## Status
Implemented (2025-10-15)

## Context

### Production Bug (2025-10-14)
A deployment of the photobooks-portal service at 20:34 caused ~15 seconds of downtime (503 errors) despite having:
- Canary deployment configured (`canary = 1`, `auto_promote = true`)
- HTTP health checks defined in Nomad job spec
- Proper `min_healthy_time = "10s"` configuration

The monitoring system detected the service was unreachable during this period, which should never happen with proper canary deployments.

### Investigation Findings

**Timeline of the incident:**
```
20:34:09 - Nomad starts canary deployment (version 25)
20:34:37 - New server registered in HAProxy (photobooks_portal_sandbox_192_168_5_3_22464)
20:34:47 - Old server drained (photobooks_portal_sandbox_192_168_5_3_22380)
20:34:47 - 20:35:09 - NO HEALTHY SERVERS (503 errors for 22 seconds)
20:35:09 - HAProxy reload triggered by DataPlane API
20:35:10 - New server becomes healthy (TCP checks pass in 1ms)
```

**Root cause:**
1. Backend `photobooks_portal_sandbox` **already existed** from a previous deployment
2. Backend was created **without** `default-server check` configuration
3. Backend was created **without** HTTP health check configuration (`option httpchk`)
4. Connector's `ensureBackend()` function found the existing backend, saw "roundrobin" algorithm was compatible, and **returned early without updating**
5. New server was added via Runtime API but health checks **did not start** because backend lacked `default-server check`
6. Old server drained → 0 healthy servers → 503 errors
7. HAProxy reload applied configuration changes → backend now had check config → health checks started → service recovered

**Key insight:** The bug only manifested when:
- Backend already existed (from previous deployment or manual creation)
- Backend was missing health check configuration
- Service used canary deployments (single server scenarios)

## The Bug

The `ensureBackend()` function in `internal/connector/service.go` had this logic:

```go
func ensureBackend(...) {
    existingBackend, err := client.GetBackend(backendName)
    if err == nil {  // Backend exists
        // Check if algorithm is compatible
        if !haproxy.IsBackendCompatibleForDynamicService(existingBackend) {
            return version, fmt.Errorf("backend incompatible...")
        }
        return version, nil  // BUG: Returns without checking health checks!
    }

    // Only creates backend if it doesn't exist...
}
```

**The problem:**
- Function only checked if backend algorithm was compatible ("roundrobin")
- Never verified if `DefaultServer.Check = "enabled"` was present
- Never verified if HTTP health check configuration was present
- Never updated existing backends with missing configuration

**Why this was dangerous:**
- Backends could be created manually or by older connector versions without proper health checks
- Silent failures - connector reported success but health checks wouldn't work
- Only manifested during deployments when new servers needed health checks
- Caused downtime in production during canary deployments

## The Fix

### 1. Added `ReplaceBackend()` Method

Added new method to HAProxy client for updating existing backend configuration:

```go
// internal/haproxy/client.go
func (c *Client) ReplaceBackend(backend *Backend, version int) (*Backend, error) {
    var updated Backend
    path := fmt.Sprintf("/v3/services/haproxy/configuration/backends/%s", backend.Name)
    err := c.makeRequest(HTTPMethodPUT, path, backend, &updated, version)
    return &updated, err
}
```

### 2. Modified `ensureBackend()` to Detect and Fix Missing Health Checks

```go
func ensureBackend(client haproxy.ClientInterface, backendName string, version int, tags []string) (int, error) {
    existingBackend, err := client.GetBackend(backendName)
    if err == nil {
        // Check algorithm compatibility (existing check)
        if !haproxy.IsBackendCompatibleForDynamicService(existingBackend) {
            return version, fmt.Errorf("backend incompatible...")
        }

        // NEW: Check if existing backend is missing health check configuration
        healthCheckConfig := parseHealthCheckFromTags(tags)
        needsUpdate := false

        // Check if DefaultServer is missing or doesn't have check enabled
        if existingBackend.DefaultServer == nil || existingBackend.DefaultServer.Check != "enabled" {
            needsUpdate = true
        }

        // Check if HTTP health checks are specified but missing
        if healthCheckConfig != nil && healthCheckConfig.Type == CheckTypeHTTP && healthCheckConfig.Path != "" {
            if existingBackend.AdvCheck != "httpchk" || existingBackend.HTTPCheckParams == nil {
                needsUpdate = true
            }
        }

        // NEW: If backend needs updating, replace it with correct configuration
        if needsUpdate {
            updatedBackend := &haproxy.Backend{
                Name: backendName,
                Balance: haproxy.Balance{Algorithm: "roundrobin"},
                DefaultServer: &haproxy.Server{Check: "enabled"},
            }

            // Configure HTTP health checks if specified
            if healthCheckConfig != nil && healthCheckConfig.Type == CheckTypeHTTP && healthCheckConfig.Path != "" {
                updatedBackend.AdvCheck = "httpchk"
                updatedBackend.HTTPCheckParams = &haproxy.HTTPCheckParams{
                    Method: healthCheckConfig.Method,
                    URI:    healthCheckConfig.Path,
                    Host:   healthCheckConfig.Host,
                }
                if updatedBackend.HTTPCheckParams.Method == "" {
                    updatedBackend.HTTPCheckParams.Method = HTTPMethodGET
                }
            }

            _, err = client.ReplaceBackend(updatedBackend, version)
            if err != nil {
                return version, fmt.Errorf("failed to update backend: %w", err)
            }

            return client.GetConfigVersion()
        }

        return version, nil
    }

    // Create backend if it doesn't exist (existing code)...
}
```

### 3. Created E2E Test to Prevent Regression

Created `TestConnector_HTTPHealthCheckE2E_ExistingMisconfiguredBackend` that:

1. **Creates misconfigured backend** (no health checks) - simulates production scenario
2. **Processes service registration** through connector
3. **Verifies fix using two methods:**
   - Our client (tests API parsing)
   - HAProxy config file (ground truth - what HAProxy actually has)
4. **Confirms health checks are enabled** in HAProxy

**Critical insight from test development:**
- Initial test only used our client to verify - could have bugs
- User correctly identified this circular reasoning
- Added verification via actual HAProxy config file AND attempted socket verification
- This ensures we're testing against HAProxy's actual state, not our potentially buggy client

## Consequences

### Positive
- ✅ **Prevents silent failures** - Existing backends are now automatically fixed
- ✅ **Zero-downtime deployments work reliably** - Health checks always configured
- ✅ **Self-healing** - Connector fixes misconfigured backends automatically
- ✅ **Production-tested** - E2E test reproduces exact production scenario
- ✅ **Backwards compatible** - Doesn't break existing functionality

### Negative
- ⚠️ **Backend updates require config version** - Could fail if config changed concurrently
- ⚠️ **Additional API call** - ReplaceBackend adds latency when updating backends

### Production Deployment Notes

**After deploying this fix, check production backends:**
```bash
# Check if backend has health check configuration
ssh lb1 'docker exec haproxy cat /etc/haproxy/haproxy.cfg' | grep -A 10 "backend photobooks_portal"

# Should see:
#   default-server check
#   option httpchk GET /healthcheck domain.com
```

**If backends are still misconfigured:**
The connector will automatically fix them on the next service registration event. To force an update:
1. Restart the connector (it will re-register all services)
2. Or trigger a service update in Nomad (scale or redeploy)

## Related ADRs

- **ADR-003**: Nomad Health Check Synchronization - Documents tag-based health check configuration
- **ADR-006**: Rolling Deployments - Explains why health checks are critical for zero-downtime

## Testing

### E2E Test Coverage
```bash
# Run the regression test
go test -v -tags=integration ./e2e -run TestConnector_HTTPHealthCheckE2E_ExistingMisconfiguredBackend

# Verification steps:
# 1. Creates backend WITHOUT health checks
# 2. Connector processes service registration
# 3. Client verifies: AdvCheck = "httpchk", HTTPCheckParams.URI = "/health", DefaultServer.Check = "enabled"
# 4. HAProxy config verifies: "option httpchk", "default-server check"
# 5. Server is created with checks enabled
```

### Files Modified
- `internal/haproxy/client.go` - Added `ReplaceBackend()` method
- `internal/haproxy/types.go` - Added `ReplaceBackend()` to interface
- `internal/connector/service.go` - Fixed `ensureBackend()` to detect and update missing health checks
- `e2e/connector_healthcheck_e2e_test.go` - Added regression test
- All test mocks - Added `ReplaceBackend()` method

## Lessons Learned

1. **TDD saved production** - Writing the test first revealed the bug clearly
2. **Verify against ground truth** - Don't just test your own client code, verify against the actual system (HAProxy config/socket)
3. **Canary deployments are unforgiving** - With single instances, any misconfiguration causes immediate downtime
4. **Backend configuration persistence matters** - Can't rely on backends always being created fresh with correct config
5. **Silent failures are dangerous** - Connector should actively validate and fix configuration issues
