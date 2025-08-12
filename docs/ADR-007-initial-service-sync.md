# ADR-007: Initial Service Synchronization on Connector Startup

**Status:** Implemented  
**Date:** 2025-08-12  
**Deciders:** Engineering Team  

## Context

On 2025-08-12, photobooks.com went down after deploying the new haproxy-nomad-connector with graceful shutdown functionality. Investigation revealed that the `photobooks_web` HAProxy backend was missing despite the corresponding Nomad service (`photobooks-web`) being active and healthy.

### Root Cause Analysis

1. **Missing Backend**: HAProxy stats showed only `nginx` and `ps_webforge` backends, but no `photobooks_web` backend
2. **Service Exists in Nomad**: `nomad service info photobooks-web` showed an active instance at `192.168.5.14:20831`
3. **No Registration Events**: Connector logs showed only `ps-webforge` service events during startup, no `photobooks-web` events
4. **Event-Only Approach**: Current connector implementation relies solely on Nomad event stream without initial sync

### Previous Implementation Gap

The connector's `GetServices()` method in `internal/nomad/client.go:185-191` was intentionally disabled:

```go
func (c *Client) GetServices() ([]*Service, error) {
    // For now, return empty slice - we'll rely on event stream for service discovery
    // This can be improved later when we sort out the exact API structure
    c.logger.Printf("Initial sync disabled - relying on event stream for service discovery")
    return []*Service{}, nil
}
```

This meant when the connector started, it only processed new service registration/deregistration events but didn't restore services that were already registered in Nomad before the connector started.

## Problem Statement

**Reliability Issue**: HAProxy backends can become missing if:
- Connector restarts while services are running in Nomad
- HAProxy configuration reloads remove dynamically created backends
- Network interruptions cause missed service events

**Business Impact**: Service outages when critical backends are not restored automatically.

## Decision

Implement initial service synchronization on connector startup to restore all existing Nomad services to HAProxy backends.

### Solution Components

1. **Initial Sync Implementation**
   - Enable `GetServices()` method to fetch all active Nomad services
   - Process existing services during connector startup
   - Ensure idempotent backend/server creation

2. **Hybrid Approach**
   - Perform initial sync on startup (restore existing state)
   - Continue event-driven processing for runtime changes
   - Handle both scenarios gracefully

3. **Error Handling**
   - Log sync progress and failures
   - Continue startup even if some services fail to sync
   - Retry mechanism for failed services

### Implementation Plan

1. **Phase 1: Implement GetServices()**
   ```go
   func (c *Client) GetServices() ([]*Service, error) {
       // Use Nomad API to list all services
       // Convert to internal Service structure
       // Return for processing
   }
   ```

2. **Phase 2: Add Initial Sync to Connector**
   ```go
   func (c *Connector) performInitialSync() error {
       services, err := c.nomadClient.GetServices()
       // Process each service through existing event handling
       // Log sync results
   }
   ```

3. **Phase 3: Integration**
   - Call initial sync during connector startup
   - Add monitoring/alerting for sync failures
   - Document the hybrid approach

## Consequences

### Positive
- **Reliability**: Services automatically restored on connector restart
- **Operational Safety**: Reduced risk of missing backends
- **Zero Manual Intervention**: No need for manual service restarts to trigger events

### Negative
- **Startup Time**: Additional delay during connector initialization
- **API Load**: Extra calls to Nomad API during startup
- **Complexity**: Hybrid event-driven + sync approach

### Risks and Mitigations
- **Risk**: Duplicate backend creation during sync
  - **Mitigation**: Idempotent operations in HAProxy client
- **Risk**: Large service count causing slow startup
  - **Mitigation**: Async processing and timeout handling
- **Risk**: Nomad API failures during sync
  - **Mitigation**: Graceful degradation, event stream as fallback

## Implementation

**Completed:** 2025-08-12

### Changes Made

1. **Enhanced GetServices() Method** (`internal/nomad/client.go:185-230`)
   - Lists all service names grouped by namespace using `client.Services().List()`
   - Retrieves detailed service registrations for each service using `client.Services().Get()`
   - Converts Nomad API responses to internal Service structs
   - Handles errors gracefully with logging

2. **Initial Sync Integration** (`internal/connector/connector.go:77-80, 142-172`)
   - Added `syncExistingServices()` call during connector startup
   - Processes existing services as fake "ServiceRegistration" events
   - Uses existing event processing logic for consistency
   - Logs sync progress and results

3. **Comprehensive Testing**
   - Unit tests for `GetServices()` method
   - Integration tests with live Nomad instance
   - Validated against real environment with 9 services

### Code Changes
```go
// New GetServices implementation
func (c *Client) GetServices() ([]*Service, error) {
    // Lists all service stubs by namespace
    serviceListStubs, _, err := c.client.Services().List(nil)
    
    // Gets detailed registrations for each service
    for _, listStub := range serviceListStubs {
        for _, serviceStub := range listStub.Services {
            serviceRegistrations, _, err := c.client.Services().Get(serviceStub.ServiceName, nil)
            // Convert to internal Service structs
        }
    }
}
```

## Implementation Timeline

- **✅ Completed**: Implement `GetServices()` method and add tests  
- **✅ Completed**: Add initial sync to connector startup  
- **✅ Completed**: Integration testing and validation  
- **🔄 Next**: Production deployment and monitoring

## Monitoring and Success Criteria

1. **Metrics**
   - Initial sync duration
   - Number of services restored
   - Sync failure rate

2. **Alerts**
   - Connector startup failures
   - Services missing after sync
   - High sync duration

3. **Success Criteria**
   - Zero manual intervention needed after connector restarts
   - All existing Nomad services restored within 30 seconds
   - Less than 1% sync failure rate

## References

- **Related**: ADR-006 (Graceful Shutdown Coordination)
- **Incident**: 2025-08-12 photobooks.com outage
- **Code**: `internal/nomad/client.go:185-191`