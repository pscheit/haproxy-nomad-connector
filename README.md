# haproxy-nomad-connector

🔗 **The missing HashiCorp integration** - Automatic HAProxy configuration from Nomad service discovery.

Like Traefik's service discovery, but for HAProxy users who want performance + control.

## ⚡ Quick Start

```bash
# Start development environment  
docker compose -f docker-compose.dev.yml up -d

# Run integration tests
go test -tags=integration -v ./test/

# Build connector
go build ./cmd/haproxy-nomad-connector

# Run connector
./haproxy-nomad-connector
```

## 🎯 What It Does

Automatically configures HAProxy based on Nomad service registrations:

1. **Nomad service registers** with `haproxy.*` tags
2. **Connector processes event** and classifies service type  
3. **HAProxy config updated** via Data Plane API
4. **Domain mapping updated** (optional) - automatic domain-to-backend mapping
5. **Changes persist** automatically - survives restarts

```nomad
# Nomad job file
service {
  name = "api-service"
  port = 8080
  tags = [
    "haproxy.enable=true",
    "haproxy.backend=dynamic", 
    "haproxy.domain=api.example.com",  # NEW: Automatic domain mapping
    "haproxy.check.path=/health"
  ]
}
```

↓ **Becomes HAProxy config + domain mapping:**

```haproxy
# HAProxy backend (via Data Plane API)
backend api_service
  balance roundrobin
  server api_service_1 192.168.1.10:8080 check
```

```
# Domain-backend map file (auto-generated)
api.example.com                api_service
```

## 🏗️ Architecture

### Service Classification
- **`dynamic`** - Creates new backends automatically
- **`custom`** - Adds servers to existing static backends
- **`static`** - No connector involvement

### Configuration Tags
- `haproxy.enable=true` - Enable HAProxy integration
- `haproxy.backend=dynamic|custom` - Backend type
- `haproxy.check.path=/health` - Health check path
- `haproxy.check.method=GET` - Health check method
- `haproxy.check.host=api.internal` - Health check host header

## 🧪 Development

Uses **Test-Driven Development** with real HAProxy integration:

```bash
# Run all tests
go test ./...

# Integration tests (requires running HAProxy)
go test -tags=integration ./test/

# Start development stack
docker compose -f docker-compose.dev.yml up -d
```

### Development Stack
- **HAProxy 3.0** with Data Plane API enabled
- **Test backend service** (nginx)  
- **Shared volumes** for config file access
- **Authentication** via userlist

## 📋 Requirements

- **HAProxy 2.0+** with Data Plane API
- **Nomad cluster** with service discovery
- **Go 1.21+** for building

## 🚀 Installation

### Docker
```bash
docker run -d \
  -e NOMAD_ADDR=http://nomad:4646 \
  -e HAPROXY_DATAPLANE_URL=http://haproxy:5555 \
  -e HAPROXY_USERNAME=admin \
  -e HAPROXY_PASSWORD=secret \
  haproxy-nomad-connector
```

### Binary
```bash
go install github.com/pscheit/haproxy-nomad-connector/cmd/haproxy-nomad-connector@latest
```

## 📖 Configuration

Environment variables or JSON config file:

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
    "password": "adminpwd",
    "backend_strategy": "use_existing"
  }
}
```

### Backend Strategy Options

Controls how the connector handles existing backends:

- **`use_existing`** (default) - Use existing compatible backends, create new ones if needed
- **`create_new`** - Always create new backends, fail if they already exist  
- **`fail_on_conflict`** - Fail fast with clear error if backend already exists

### Backend Compatibility

For dynamic services, the connector checks that existing backends have:
- `balance roundrobin` algorithm
- Compatible configuration for dynamic server management

If an incompatible backend exists, the connector will fail with a clear error message instead of silently ignoring the issue.

## 🌐 Domain Mapping

**NEW:** Automatic domain-to-backend mapping eliminates manual HAProxy map file management!

### Quick Setup

```bash
# Enable domain mapping in config
export DOMAIN_MAP_ENABLED=true
export DOMAIN_MAP_FILE_PATH=/etc/haproxy2/domain-backend.map

# Or in config.json:
{
  "domain_map": {
    "enabled": true,
    "file_path": "/etc/haproxy2/domain-backend.map"
  }
}
```

### How It Works

Add `haproxy.domain` tag to your Nomad services:

```hcl
service {
  name = "crm-prod"
  port = 8080
  tags = [
    "haproxy.enable=true",
    "haproxy.backend=dynamic",
    "haproxy.domain=crm.ps-webforge.net"  # Automatic domain mapping!
  ]
}
```

The connector automatically:

1. **Service registers** → Creates `crm_prod` backend
2. **Domain mapping created** → Adds `crm.ps-webforge.net → crm_prod` to map file
3. **Service deregisters** → Removes domain mapping when no servers remain

### Advanced Domain Types

```hcl
# Exact domain match (default)
"haproxy.domain=api.example.com"

# Prefix matching  
"haproxy.domain=api.example.com"
"haproxy.domain.type=prefix"

# Regex patterns
"haproxy.domain=.*\\.assets\\.example\\.com"
"haproxy.domain.type=regex"
```

### Benefits

- ✅ **Zero manual map file editing**
- ✅ **GitOps friendly** - domains defined in Nomad jobs
- ✅ **Automatic cleanup** - removes stale mappings
- ✅ **Atomic updates** - consistent map file generation
- ✅ **Backward compatible** - works alongside manual entries

## 🔍 Comparison

| Feature | Traefik | haproxy-nomad-connector |
|---------|---------|-------------------------|
| Service Discovery | ✅ Built-in | ✅ This project |
| Domain Mapping | ✅ Built-in | ✅ **NEW: Automatic** |
| Performance | Good | ⚡ Excellent (HAProxy) |  
| Configuration | Limited | 🎯 Full HAProxy power |
| Persistence | Memory | 💾 Config files |
| Load Balancing | Basic | 🏋️ Advanced algorithms |
| SSL/TLS | Built-in | 🔒 HAProxy's best-in-class |

## 🤝 Contributing

Built with TDD - tests are first-class citizens:

1. Write failing test
2. Implement feature  
3. Ensure all tests pass
4. Submit PR

See integration tests for examples of the expected behavior.

## 📝 Status

**Production Ready** - Core functionality complete:

- ✅ Data Plane API integration
- ✅ Service classification from tags
- ✅ Dynamic backend creation with conflict detection
- ✅ Server lifecycle management
- ✅ Configuration persistence
- ✅ Nomad event stream listener
- ✅ Backend compatibility checking
- ✅ **NEW: Automatic domain mapping** - eliminates manual map file management
- ✅ Robust error handling and clear error messages
- 🔄 Custom backend support (planned)
- 🔄 Advanced health check config (planned)

---

Built to solve the gap between Nomad's service discovery and HAProxy's performance.