job "legacy-service" {
  datacenters = ["dc1"]
  
  group "web" {
    count = 2
    
    service {
      name = "legacy-web"
      port = "http"
      
      # Custom backend - uses existing HAProxy backend config
      tags = [
        "haproxy.enable=true",
        "haproxy.backend=custom",
        "haproxy.backend.name=legacy_web_backend"
      ]
      
      check {
        type     = "tcp"
        interval = "10s"
        timeout  = "3s"
      }
    }
    
    task "web" {
      driver = "exec"
      
      config {
        command = "/app/legacy-service"
        args    = ["--port", "${NOMAD_PORT_http}"]
      }
      
      resources {
        cpu    = 200
        memory = 256
      }
    }
    
    network {
      port "http" {}
    }
  }
}

# This requires existing HAProxy backend configuration:
#
# backend legacy_web_backend
#   balance leastconn
#   option httpchk GET /status HTTP/1.1\r\nHost:\ legacy.internal
#   http-check expect status 200
#   # Servers will be added dynamically by connector:
#   # server legacy_web_192_168_1_20_8080 192.168.1.20:8080 check
#   # server legacy_web_192_168_1_21_8080 192.168.1.21:8080 check