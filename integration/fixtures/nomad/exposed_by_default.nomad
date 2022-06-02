job "who" {
  datacenters = ["dc1"]

  group "proxy" {
    network {
      mode = "host"
      port "ingress" {
        static = 8899
      }
    }

    task "traefik" {
      driver = "raw_exec"
      config {
        command = "EXECUTABLE"
        args    = [
          "--log.level=DEBUG",
          "--entryPoints.web.address=:8899",
          "--providers.nomad.refreshInterval=1s",
          "--api.insecure",
        ]
      }

      resources {
        cpu    = 10
        memory = 32
      }
    }
  }

  group "who-default" {
    network {
      port "http" {
        to = 80
      }
    }

    service {
      name     = "whoami"
      provider = "nomad"
      port     = "http"
      tags     = [] // Enabled by default.
    }

    task "whoami" {
      driver = "docker"

      config {
        image = "traefik/whoami:v1.8.0"
        args  = [
          "-verbose",
          "-name",
          "whoami-default",
        ]
      }

      resources {
        cpu    = 10
        memory = 32
      }
    }
  }

  group "who-disable" {
    network {
      port "http" {
        to = 80
      }
    }

    service {
      name     = "whoami2"
      provider = "nomad"
      port     = "http"
      tags     = [
        "traefik.enable=false",
      ]
    }

    task "whoami" {
      driver = "docker"

      config {
        image = "traefik/whoami:v1.8.0"
        args  = [
          "-verbose",
          "-name",
          "whoami-disabled",
        ]
      }

      resources {
        cpu    = 10
        memory = 32
      }
    }
  }
}
