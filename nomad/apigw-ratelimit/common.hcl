app_count = 1
job_name  = "apigw-ratelimit"

docker_image        = "kentik-api-ratelimit"
consul_service_name = "apigw-ratelimit"

consul_service = {
  enabled            = true
  service_name       = "apigw-ratelimit"
  service_port_label = "grpc"
  service_tags       = [],
  check_type         = "grpc"
  check_path         = "/hc"
  check_interval     = "5s"
  check_timeout      = "1s"
}

env_vars = [
  {
    key   = "USE_STATSD"
    value = "false"
  },
  {
    key   = "REDIS_SOCKET_TYPE"
    value = "tcp"
  },
  {
    key   = "REDIS_URL"
    value = "redis://$${attr.unique.network.ip-address}:9489/11"
  },
  {
    key   = "RUNTIME_ROOT"
    value = "/run"
  },
  {
    key   = "RUNTIME_SUBDIRECTORY"
    value = "ratelimit"
  },
  {
    key   = "RUNTIME_IGNOREDOTFILES"
    value = "true"
  },
  {
    key   = "MAX_SLEEPING_ROUTINES"
    value = "64"
  },
  {
    key   = "GRPC_PORT"
    value = "9543"
  },
  {
    key   = "PORT",
    value = "9485"
  }
]

network = {
  mode   = "host"
  static = true
  ports = {
    "grpc" = 9543
  }
}

env_secrets = [
  {
    source = "kt_secrets::redis_api_general_master_password"
    prefix = "hiera/data/puppet"
    dest   = "REDIS_AUTH"
  }
]

volumes = {
  "apigw-ratelimit-config" = {
    type        = "host"
    source      = "apigw-ratelimit-config"
    destination = "/run"
    read_only   = true
  }
}

args = [
  "/usr/bin/ratelimit-server"
]

// registry.our1.kentik.com:5000/kentik-api-ratelimit:nomad.7
// registry.our1.kentik.com:5000/kentik-api-ratelimit
// registry.our1.kentik.com:5000/kentik-apigw-ratelimit:nomad.7
