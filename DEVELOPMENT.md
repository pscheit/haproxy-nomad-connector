# Development Guide

## Quick Start

### Prerequisites
- Go 1.21+
- Docker and Docker Compose
- Running HAProxy with Data Plane API

### Development Environment

```bash
# Start HAProxy + Data Plane API + test backend
docker compose -f docker-compose.dev.yml up -d

# Build connector
go build ./cmd/haproxy-nomad-connector

# Run connector (requires running Nomad cluster)
export NOMAD_ADDR=http://localhost:4646
export HAPROXY_DATAPLANE_URL=http://localhost:5555
export HAPROXY_USERNAME=admin
export HAPROXY_PASSWORD=adminpwd

./haproxy-nomad-connector
```

### Testing

```bash
# Unit tests
go test ./...

# Integration tests (requires running HAProxy)
go test -tags=integration ./test/

# Check Data Plane API connection
curl -u admin:adminpwd http://localhost:5555/v3/info

# Check connector health (when running)
curl http://localhost:8080/health
curl http://localhost:8080/metrics
```

### Project Structure

```
├── cmd/haproxy-nomad-connector/    # Main application
├── internal/
│   ├── config/                     # Configuration management
│   ├── connector/                  # Core connector logic
│   ├── haproxy/                    # HAProxy Data Plane API client
│   └── nomad/                      # Nomad event stream client
├── test/                           # Integration tests
├── dev/                            # Development configs (HAProxy, etc.)
└── docker-compose.dev.yml          # Local development stack
```

### Adding Features

1. **Write failing test** - Start with test-driven development
2. **Implement feature** - Focus on single responsibility
3. **Run all tests** - Ensure nothing breaks
4. **Update documentation** - Keep README current

### HAProxy Data Plane API

The connector communicates with HAProxy via its Data Plane API:

- **Port**: 5555 (configurable)
- **Authentication**: Basic auth (admin/adminpwd in dev)
- **Config persistence**: Automatic via Data Plane API
- **Version management**: Handled automatically

Key endpoints used:
- `GET /v3/info` - API health check
- `GET /v3/services/haproxy/configuration/backends` - List backends
- `POST /v3/services/haproxy/configuration/backends` - Create backend
- `POST /v3/services/haproxy/configuration/backends/{name}/servers` - Add server

### Nomad Integration

The connector listens to Nomad's event stream:

- **Event Stream**: `/v1/event/stream?topic=Service`
- **Authentication**: X-Nomad-Token header (if token provided)
- **Event Types**: ServiceRegistration, ServiceDeregistration
- **Auto-reconnect**: On connection loss

### Service Classification

Services are classified based on tags:

- **`haproxy.enable=true`** - Required for processing
- **`haproxy.backend=dynamic`** - Create new backend
- **`haproxy.backend=custom`** - Use existing backend
- **No tags** - Static (ignored)

### Configuration

Via environment variables or JSON config file:

```json
{
  "nomad": {
    "address": "http://localhost:4646",
    "token": "",
    "region": "global"
  },
  "haproxy": {
    "address": "http://localhost:5555",
    "username": "admin", 
    "password": "adminpwd"
  }
}
```

### Troubleshooting

**Connection Issues**:
- Check HAProxy Data Plane API is running on port 5555
- Verify userlist configured in HAProxy config
- Check authentication credentials

**Event Stream Issues**:
- Verify Nomad cluster is running and accessible
- Check Nomad token permissions for event stream access
- Monitor connector logs for reconnection attempts

**Backend Creation Issues**:
- Verify HAProxy config file is writable by Data Plane API
- Check Data Plane API logs for configuration errors
- Ensure proper version parameter handling