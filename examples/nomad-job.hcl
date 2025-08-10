job "api-service" {
  datacenters = ["dc1"]
  
  group "api" {
    count = 3
    
    service {
      name = "api-service"
      port = "http"
      
      # HAProxy integration tags
      tags = [
        "haproxy.enable=true",
        "haproxy.backend=dynamic",
        "haproxy.check.path=/health",
        "haproxy.check.method=GET"
      ]
      
      check {
        type     = "http"
        path     = "/health"
        interval = "10s"
        timeout  = "3s"
      }
    }
    
    task "api" {
      driver = "docker"
      
      config {
        image = "nginx:alpine"
        ports = ["http"]
      }
      
      resources {
        cpu    = 100
        memory = 128
      }
      
      env {
        PORT = "${NOMAD_PORT_http}"
      }
    }
    
    network {
      port "http" {
        to = 80
      }
    }
  }
}

# This job will result in HAProxy configuration:
#
# backend api_service
#   balance roundrobin
#   server api_service_192_168_1_10_8080 192.168.1.10:8080 check
#   server api_service_192_168_1_11_8080 192.168.1.11:8080 check
#   server api_service_192_168_1_12_8080 192.168.1.12:8080 check