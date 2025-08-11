# ADR-003: Synchronize Health Checks from Nomad Job Specifications

## Status
Proposed

## Context

Currently, the haproxy-nomad-connector creates all HAProxy backend servers with basic Layer4 health checks (`check enabled`), regardless of the actual health check configuration defined in Nomad job specifications. This creates several issues:

1. **Inconsistency**: Health checks in HAProxy don't match those defined in Nomad, leading to different failure detection behavior
2. **Suboptimal checks**: Services with HTTP endpoints use TCP checks instead of HTTP, missing application-level failures
3. **Manual configuration**: Operators must use tags (`haproxy.check.*`) to configure health checks, duplicating what's already in Nomad
4. **Rolling deployment issues**: Mismatched health checks cause unnecessary downtime during deployments

Nomad job specifications already contain comprehensive health check definitions:

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

## Decision

We will enhance the connector to automatically read and apply health check configurations from Nomad job specifications to HAProxy backend servers.

### Implementation Approach

1. **Query Job Specification**: When processing a ServiceRegistration event, query the Nomad API for the job specification
2. **Extract Check Configuration**: Parse the service's check stanza from the job spec
3. **Map to HAProxy Configuration**: Convert Nomad check types to HAProxy equivalents:
   - Nomad `http` → HAProxy `httpchk`
   - Nomad `tcp` → HAProxy `check` (default)
   - Nomad `script` → Skip or use TCP fallback
   - Nomad `grpc` → HAProxy `check` with custom options

4. **Apply Configuration**: Configure the HAProxy server with appropriate health check parameters

### API Changes

The `haproxy.Server` struct will be extended:

```go
type Server struct {
    Name        string
    Address     string
    Port        int
    Check       string
    CheckType   string  // "tcp", "http", "ssl", "mysql", etc.
    CheckPath   string  // For HTTP checks
    CheckMethod string  // GET, HEAD, etc.
    CheckHost   string  // Host header for HTTP checks
    CheckInter  int     // Interval in milliseconds
    CheckRise   int     // Consecutive successes to mark UP
    CheckFall   int     // Consecutive failures to mark DOWN
}
```

### Fallback Behavior

1. If job spec query fails → Use current behavior (basic TCP check)
2. If no check defined in Nomad → Use basic TCP check
3. If check type unsupported → Log warning and use TCP check
4. Tags override job spec → `haproxy.check.*` tags take precedence

## Consequences

### Positive

- **Consistency**: HAProxy health checks match Nomad's configuration
- **Zero-downtime deployments**: Proper health checks reduce rolling deployment issues
- **Less configuration**: No need to duplicate health check config in tags
- **Better failure detection**: HTTP checks catch application-level failures
- **Single source of truth**: Nomad job spec becomes authoritative for health checks

### Negative

- **Additional API calls**: Each service registration requires querying job specs
- **Increased complexity**: More code paths and error scenarios
- **Potential latency**: Job spec queries add processing time
- **Backward compatibility**: Must maintain support for existing tag-based configuration

### Mitigation Strategies

1. **Cache job specs**: Cache recently queried job specifications with TTL
2. **Async processing**: Query job specs asynchronously to avoid blocking
3. **Feature flag**: Add config option to enable/disable job spec synchronization
4. **Graceful degradation**: Always fall back to basic checks on errors

## Implementation Plan

### Phase 1: Core Implementation
- Add Nomad job query client methods
- Implement check stanza parser
- Create HAProxy check configuration mapper
- Update server creation logic

### Phase 2: Optimization
- Add job spec caching layer
- Implement async job spec fetching
- Add metrics for monitoring

### Phase 3: Advanced Features
- Support for gRPC health checks
- Custom check scripts via sidecar pattern
- Multi-port service health checks

## References

- [Nomad Service Checks Documentation](https://developer.hashicorp.com/nomad/docs/job-specification/check)
- [HAProxy Health Checks Documentation](http://cbonte.github.io/haproxy-dconv/2.6/configuration.html#5.2-check)
- [Issue #X: Services fail during rolling deployments]