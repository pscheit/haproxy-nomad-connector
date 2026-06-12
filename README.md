# haproxy-nomad-connector

🔗 **The missing HashiCorp integration** - Automatic HAProxy configuration from Nomad service discovery.

Like Traefik's service discovery, but for HAProxy users and Nomad standalone users (without Consul service discovery). 

## ⚡ Quick Start

```bash
go build ./cmd/haproxy-nomad-connector

# copy binary on to your Linux amd64 based system
./haproxy-nomad-connector
```

## 🎯 What It Does

Automatically configures HAProxy based on Nomad service registrations:

1. **Nomad service registers** with `haproxy.*` tags
2. **Connector processes event** and classifies service type  
3. **HAProxy config updated** via Data Plane API
4. **Frontend rules updated** (optional) - automatic host-based routing rules
5. **Changes persist** automatically - survives restarts

```nomad
# Nomad job file
service {
  name = "api-service"
  port = 8080
  tags = [
    "haproxy.enable=true",
    "haproxy.backend=dynamic", 
    "haproxy.domain=api.example.com",
    "haproxy.check.path=/health"
  ]
}
```

↓ **Becomes HAProxy config + frontend rules:**

```haproxy
# HAProxy backend (via Data Plane API)
backend api_service
  balance roundrobin
  server api_service_1 192.168.1.10:8080 check
```

```haproxy
frontend https
  bind *:443 ssl crt /etc/ssl/certs/
  use_backend api_service if { hdr(host) -i api.example.com }
```
(you have to add the frontend to your haproxy cfg on your own)

## 🏷️ Nomad Service Tags Reference

The connector uses Nomad service tags to control HAProxy integration. Add these tags to your Nomad service definitions:

### Core Control Tags
- **`haproxy.enable=true`** - Enable HAProxy integration (required)
- **`haproxy.backend=dynamic|custom`** - Backend management strategy:
  - `dynamic` - Creates new backends automatically (default)
  - `custom` - Adds servers to existing static backends

### Frontend Routing Tags  
- **`haproxy.domain=example.com`** - Domain for automatic frontend rule creation
- **`haproxy.domain.type=exact|prefix|regex`** - Domain matching type:
  - `exact` - Exact domain match (default)
  - `prefix` - Prefix matching for subdomains
  - `regex` - Regular expression patterns

### Health Check Tags
- **`haproxy.check.path=/health`** - HTTP health check endpoint path
- **`haproxy.check.method=GET`** - HTTP health check method (default: GET)  
- **`haproxy.check.host=api.internal`** - Host header for health checks
- **`haproxy.check.type=http|tcp`** - Health check type:
  - `http` - HTTP health checks (default when path specified)
  - `tcp` - TCP connection health checks (default)
- **`haproxy.check.disabled`** - Disable health checks entirely

## 🧪 Development

use the makefile to run tests, linter and build.

## 📋 Requirements

- **HAProxy 3.0+** with Data Plane API (runtime server management requires 3.0+)
- **Nomad cluster** with service discovery
- **Go 1.21+** for building

⚠️  **HAProxy 2.x is NOT supported** due to incomplete DataPlane API runtime endpoints. See [ADR-010](docs/adr/010-haproxy-3-0-runtime-api-requirement.md) for details.

## 🚀 Installation

### HAProxy 3.0 Setup (Required)

**Debian/Ubuntu:**
```bash
# Add HAProxy 3.0 repository
curl -s https://haproxy.debian.net/bernat.debian.org.gpg | apt-key add -
echo "deb https://haproxy.debian.net bookworm-backports-3.0 main" > /etc/apt/sources.list.d/haproxy.list
apt update

# Install HAProxy 3.0
apt install haproxy=3.0.*
```

**Verify version:**
```bash
haproxy -v  # Should show HAProxy version 3.0.x
```

### Connector Binary
```bash
go install github.com/pscheit/haproxy-nomad-connector/cmd/haproxy-nomad-connector@latest
```

## 📖 Configuration

Environment variables or JSON config file:

```json
{
  "nomad": {
    "address": "http://localhost:4646",
    "token": "add this!",
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

The haproxy address needs to be the endpoint of your Data plane API. Look at the official docs how to run it.

**Quick Data Plane API setup:**
```bash
# Add to your haproxy.cfg
userlist haproxy-dataplaneapi
    user admin insecure-password adminpwd

program api
    command dataplaneapi --host 0.0.0.0 --port 5555 --haproxy-bin /usr/sbin/haproxy --config-file /etc/haproxy/haproxy.cfg --reload-cmd "systemctl reload haproxy" --reload-delay 5 --userlist haproxy-dataplaneapi
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
| Domain Mapping | ✅ Built-in | ✅  |
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
