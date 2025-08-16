# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is a Go service that bridges HashiCorp Nomad service discovery with HAProxy load balancing. The connector listens to Nomad's event stream and automatically manages HAProxy backends via the Data Plane API.

## Core Architecture

The system consists of three main integration layers:
- **Nomad Client** (`internal/nomad/`) - Streams service events from Nomad API
- **HAProxy Client** (`internal/haproxy/`) - Manages backends via Data Plane API
- **Connector** (`internal/connector/`) - Orchestrates the integration logic

### Service Classification System
Services are processed based on tags:
- `haproxy.enable=true` - Required for processing
- `haproxy.backend=dynamic` - Creates new backends automatically  
- `haproxy.backend=custom` - Adds servers to existing static backends
- `haproxy.domain=example.com` - Enables automatic domain mapping

### Configuration Management
Configuration is loaded via `internal/config/config.go` from:
- Environment variables (NOMAD_ADDR, HAPROXY_DATAPLANE_URL, etc.)
- JSON config files (see examples/ directory)

## Development Commands

### Build and Run
```bash
# Build binary
make build
# Or: go build ./cmd/haproxy-nomad-connector

# Run with development environment
docker compose -f docker-compose.dev.yml up -d
./build/haproxy-nomad-connector

# Install to GOPATH
make install
```

### Testing
```bash
# Unit tests
make test
# Or: go test -v -race ./...

# Integration tests (requires running HAProxy)
go test -tags=integration -v ./test/

# Coverage report
make test-coverage

# Benchmarks
make bench
```

### Code Quality
```bash
# Run all checks
make check

# Individual checks
make fmt      # Format code
make vet      # Go vet
make lint     # golangci-lint (requires installation)
```

### Development Environment
```bash
# Start development stack (HAProxy + Data Plane API + test backend)
docker compose -f docker-compose.dev.yml up -d

# Check connections
curl -u admin:adminpwd http://localhost:5555/v3/info          # HAProxy API
curl http://localhost:8080/health                            # Connector health
```

## Key Integration Points

### HAProxy Data Plane API
- **Endpoint**: Port 5555 (configurable)
- **Authentication**: Basic auth via userlist configuration
- **Key operations**: Backend creation, server management, configuration persistence
- **Client**: `internal/haproxy/client.go`

### Nomad Event Stream
- **Endpoint**: `/v1/event/stream?topic=Service`
- **Events**: ServiceRegistration, ServiceDeregistration
- **Auto-reconnection**: Handled automatically on connection loss
- **Client**: `internal/nomad/client.go`

### Domain Mapping Feature
- Automatically generates domain-to-backend map files from `haproxy.domain` tags
- Supports exact, prefix, and regex domain matching
- File generation handled in `internal/connector/mapfile.go`
- Configuration via `domain_map.enabled` and `domain_map.file_path`

## Test-Driven Development

This codebase follows TDD principles:
1. Write failing tests first
2. Implement minimal code to pass
3. Refactor while keeping tests green
4. Integration tests validate real HAProxy interactions

Run tests frequently during development and ensure integration tests pass before submitting changes.

## Backend Strategy Configuration

The `haproxy.backend_strategy` setting controls conflict resolution:
- `use_existing` (default) - Use compatible existing backends
- `create_new` - Always create new, fail on conflicts
- `fail_on_conflict` - Fail fast with clear errors

## Error Handling Patterns

- All errors should bubble up with context using `fmt.Errorf`
- Network operations should be retryable with exponential backoff
- Configuration validation happens early in startup
- Clear error messages for common operational issues

## ⚠️ CRITICAL: Definition of DONE

**ALWAYS use the TodoWrite tool to track tasks according to these criteria. A task is only DONE when ALL criteria are met:**

### Required DONE Criteria (ALL must be satisfied):
a) **Test ALL acceptance criteria** - Every acceptance criteria from the ADR/ticket must have corresponding tests
b) **Failing tests first** - Tests must initially fail and reproduce the exact bug/requirement  
c) **Implement solution** - Write minimal code to make tests pass
d) **Code quality** - Lint everything, verify solution with real-world test, ALL tests green (unit + integration)
e) **CI validation** - Commit, push, and verify CI passes with ALL new tests executing
f) **Production validation** - Test fix in actual production environment where issue occurred

### Mandatory Process:
1. **ALWAYS use TodoWrite tool** to explicitly track each DONE criterion (a-f) as separate todo items
2. **NEVER mark a task as "completed" or "done"** unless ALL 6 criteria are satisfied
3. **Before any commit** - run `make lint` and verify that ALL tests are passing (unit + integration)
4. **CI must pass** - It's not a matter of who did it, if CI fails you must fix it
5. **Real-world validation required** - Deploy and test in the actual environment where the issue occurred

### Example TodoWrite Usage:
```
- Test acceptance criteria A from ADR-009
- Test acceptance criteria B from ADR-009  
- Implement fix for server counting logic
- All unit tests passing
- All integration tests passing
- Code linted and formatted
- Committed and pushed
- CI pipeline green
- Production validation complete
```

**Remember: Saying "it's done" prematurely undermines quality and creates technical debt. Follow the process.**