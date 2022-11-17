app_count    = 1
job_name     = "apigw-ratelimit"
docker_image = "kentik-api-ratelimit"
use_runtime_count = true
pin_nodes = true

network = {
  mode = "host"
  ports = {
    "grpc" = {
      port       = 9543
      check_type = "grpc"
    }
    // "admin" = {
    //   port           = 9485
    //   check_disabled = true
    // }
  }
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
    value = "9484"
  },
  {
    key   = "PORT",
    value = "9485"
  }
]


env_secrets = [
  {
    source = "kt_secrets::redis_api_general_master_password"
    dest   = "REDIS_AUTH"
  }
]

volumes = {
  "apigw-ratelimit-config" = {
    type        = "host"
    source      = "apigw-ratelimit-config"
    destination = "/run/ratelimit"
    read_only   = true
  }
}

args = [
  "/usr/bin/ratelimit-server"
]
