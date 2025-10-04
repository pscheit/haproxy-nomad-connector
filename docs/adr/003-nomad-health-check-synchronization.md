# ADR-003: Synchronize Health Checks from Nomad Job Specifications

## Status
Implemented (2025-10-04)

**Modified from original proposal** - See "Actual Implementation" section below for details.

## Context

The haproxy-nomad-connector needed to support proper health check configuration from Nomad services. Initial implementation used basic TCP checks for all servers, which created several issues:

1. **Inconsistency**: Health checks in HAProxy didn't match those defined in Nomad, leading to different failure detection behavior
2. **Suboptimal checks**: Services with HTTP endpoints used TCP checks instead of HTTP, missing application-level failures
3. **Manual configuration**: Operators had to manually configure health checks
4. **Rolling deployment issues**: Mismatched health checks caused unnecessary downtime during deployments

Nomad job specifications contain comprehensive health check definitions:

```hcl
service {
  name = "web-app"
  port = "http"

  check {
    type     = "http"
    path     = "/health"
    interval = "10s"
    timeout  = "2s"
  }
}
```

## Original Decision (Proposed)

Initially proposed to query Nomad job specifications and apply health checks using per-server configuration fields. However, during implementation we discovered critical limitations with the HAProxy DataPlane API that required a different approach.

## Actual Implementation

### Critical Discovery: HAProxy DataPlane API Limitations

During implementation, we discovered that **per-server health check fields are silently ignored** by the HAProxy DataPlane API v3:

```go
// These fields DO NOT WORK when set on individual servers:
type Server struct {
    CheckType   string  // ❌ Ignored by DataPlane API
    CheckPath   string  // ❌ Ignored by DataPlane API
    CheckMethod string  // ❌ Ignored by DataPlane API
    CheckHost   string  // ❌ Ignored by DataPlane API
}
```

Testing revealed that setting these fields had no effect on HAProxy's actual health check behavior. The API accepts them but doesn't apply them to the configuration.

### Actual Approach: Backend-Level Configuration

Health checks **must be configured at the backend level**, not per-server:

```go
type Backend struct {
    Name            string
    AdvCheck        string           // "httpchk" for HTTP checks
    HTTPCheckParams *HTTPCheckParams // HTTP check configuration
    DefaultServer   *Server          // Default server parameters including check enablement
}

type HTTPCheckParams struct {
    Method  string  // "GET", "POST", etc.
    URI     string  // Health check path
    Host    string  // Host header
}
```

### Implementation Details

1. **Tag-Based Configuration**: Services specify health checks via tags instead of querying job specs:
   ```hcl
   tags = [
     "haproxy.enable=true",
     "haproxy.check.path=/health",
     "haproxy.check.host=example.com",
     "haproxy.check.method=GET"
   ]
   ```

2. **Backend Creation**: When creating backends, configure health checks at backend level:
   ```go
   backend := haproxy.Backend{
       AdvCheck: "httpchk",
       HTTPCheckParams: &haproxy.HTTPCheckParams{
           Method: "GET",
           URI:    "/health",
           Host:   "example.com",
       },
       DefaultServer: &haproxy.Server{
           Check: "enabled",  // Critical: enables checks for all servers
       },
   }
   ```

3. **The `default_server` Solution**: Setting `DefaultServer.Check = "enabled"` at the backend level enables health checks automatically for all servers added to that backend. This eliminates the need for:
   - Socket commands (`enable health backend/server`)
   - Runtime API calls to set server state
   - Per-server check configuration

4. **Health Check Types**:
   - HTTP checks: `backend.AdvCheck = "httpchk"` with `HTTPCheckParams`
   - TCP checks: `default_server.check = "enabled"` only (no AdvCheck)
   - Disabled: `haproxy.check.disabled=true` tag

### Why Not Job Spec Queries?

We opted for tag-based configuration instead of querying Nomad job specs because:

1. **Simplicity**: Tags are immediately available in service events
2. **Performance**: No additional API calls required
3. **Flexibility**: Tags can override or supplement Nomad checks
4. **Backward compatibility**: Existing tag-based configuration continues to work

## Consequences

### Positive

- ✅ **Zero-downtime deployments**: Proper health checks enable smooth rolling deployments
- ✅ **Better failure detection**: HTTP checks catch application-level failures
- ✅ **Automatic enablement**: `default_server` approach works reliably in HAProxy 3.0+
- ✅ **Simple configuration**: Tag-based configuration is intuitive
- ✅ **No socket commands**: Pure HTTP API approach eliminates complexity

### Negative

- ⚠️ **Backend-level limitation**: All servers in a backend share the same health check configuration
- ⚠️ **Tag duplication**: Health check configuration must be specified in tags if different from Nomad
- ⚠️ **No per-server customization**: Cannot have different health check paths for different servers in same backend

### Trade-offs Accepted

We accepted backend-level health check configuration as a reasonable constraint because:
- Services in the same backend typically use the same health check endpoint
- The alternative (per-server configuration) doesn't work in HAProxy DataPlane API
- Tag-based configuration provides sufficient flexibility for most use cases

## Implementation Results

### What Works

✅ **E2E Test Verification** (`e2e/connector_healthcheck_e2e_test.go`):
1. Backend created with HTTP health check configuration
2. HAProxy config contains `option httpchk GET /health example.com`
3. Server added with checks enabled via `default_server`
4. Health checks actually run and report status

✅ **Regression Test** (`health_check_enable_bug_test.go`):
- Verifies backends are created with `default_server.check=enabled`
- Prevents regression to MAINT mode issues

### Performance

- No additional API calls required (tags available in events)
- Single backend creation includes full health check configuration
- No socket commands or Runtime API calls needed

## Related ADRs

- **ADR-010**: HAProxy 3.0+ Runtime API Requirement - Documents why HAProxy 3.0+ is needed for reliable runtime operations
- **ADR-006**: Rolling Deployments - Health checks enable zero-downtime deployments

## References

- [Nomad Service Checks Documentation](https://developer.hashicorp.com/nomad/docs/job-specification/check)
- [HAProxy Health Checks Documentation](http://cbonte.github.io/haproxy-dconv/3.0/configuration.html#5.2-check)
- [HAProxy DataPlane API v3 Specification](https://www.haproxy.com/documentation/dataplaneapi/latest/)
- E2E Test: `e2e/connector_healthcheck_e2e_test.go`
- Implementation: `internal/connector/service.go` (ensureBackend, parseHealthCheckFromTags)
