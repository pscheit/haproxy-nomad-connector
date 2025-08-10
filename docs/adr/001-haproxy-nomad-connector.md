# ADR 001: HAProxy Nomad Connector Architecture

## Status
Proposed

## Context
The HashiCorp ecosystem provides excellent service discovery integration between Consul and HAProxy via the HAProxy Data Plane API. However, there is no equivalent integration for Nomad users who want to use HAProxy as their load balancer without running Consul.

Teams using Nomad + HAProxy currently have to:
- Manually configure HAProxy backends
- Use runtime API hacks for dynamic configuration
- Build custom solutions that don't persist across restarts
- Choose between HAProxy performance and Traefik-like service discovery

## Decision
Build `haproxy-nomad-connector` - an open source service discovery integration that bridges Nomad services with HAProxy via the Data Plane API.

## Architecture Vision

### What it solves
The same gap that HAProxy's Consul integration fills, but for Nomad users

### Target users
- Anyone running Nomad + HAProxy (like us)
- Teams wanting Traefik-like service discovery but with HAProxy performance  
- HashiCorp users who chose HAProxy over Consul Connect

### Key differentiators
- ✅ **Official HAProxy Data Plane API** (not custom runtime API hacks)
- ✅ **Nomad-native** (uses Nomad's event stream, not polling)
- ✅ **Service tag configuration** (following Traefik/Consul patterns)
- ✅ **Three backend types**: custom, dynamic, static
- ✅ **Production ready** (proper error handling, logging, metrics)

### Service Backend Types
1. **Custom backends**: Use existing HAProxy backend configs, connector only adds/removes servers
2. **Dynamic backends**: Connector creates entire backend configuration from service tags
3. **Static backends**: No connector involvement, purely manual configuration

## Architecture Design Questions
1. **Deployment model**: Standalone binary, Docker container, or both?
2. **Configuration**: YAML file, env vars, or command line flags?  
3. **Service tags**: What should the `haproxy.*` tag schema look like?
4. **Backend templates**: How do we handle complex HAProxy configs elegantly?

## Implementation Plan
1. Experiment with HAProxy Data Plane API to understand persistence and transaction model
2. Design service tag conventions and backend classification system
3. Build Nomad event stream listener with Data Plane API integration
4. Create proper open source project structure
5. Add production features (metrics, logging, error handling)

## Consequences
- Fills a genuine gap in the HashiCorp ecosystem
- Could become the standard solution for Nomad + HAProxy integration
- Requires understanding both Nomad event streams and HAProxy Data Plane API
- Success depends on making it as simple to use as Traefik's service discovery