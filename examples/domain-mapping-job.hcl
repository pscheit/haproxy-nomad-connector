job "crm-prod" {
  datacenters = ["dc1"]
  type        = "service"

  group "web" {
    count = 2

    network {
      port "http" {
        static = 8080
      }
    }

    service {
      name = "crm-prod"
      port = "http"
      
      # Domain mapping tags - NEW feature!
      tags = [
        "haproxy.enable=true",
        "haproxy.backend=dynamic",
        "haproxy.domain=crm.ps-webforge.net",     # Automatic domain mapping
        "haproxy.domain.type=exact",              # Optional: exact|prefix|regex
        "haproxy.check.path=/health",             # Health check endpoint
        "version=1.0.0"
      ]
      
      check {
        type     = "http"
        path     = "/health"
        interval = "10s"
        timeout  = "3s"
      }
    }

    task "web" {
      driver = "docker"

      config {
        image = "nginx:alpine"
        ports = ["http"]
      }

      env {
        PORT = "${NOMAD_PORT_http}"
      }

      resources {
        cpu    = 100
        memory = 128
      }
    }
  }
}

# What happens when you run this job:
#
# 1. Nomad registers service "crm-prod" with domain tag
# 2. haproxy-nomad-connector detects the registration event
# 3. Creates HAProxy backend "crm_prod" (dash→underscore conversion)
# 4. Adds servers for each instance: crm_prod_<ip>_<port>_8080
# 5. Updates domain map file: "crm.ps-webforge.net    crm_prod"
# 6. HAProxy can now route crm.ps-webforge.net → crm_prod backend
#
# When you stop the job:
# 1. Servers removed from backend
# 2. If no servers remain, domain mapping is also removed
# 3. Clean state - no stale entries!