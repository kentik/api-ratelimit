job_name  = "controlplane-ratelimit"
app_count = 2


constraints = [
  {
    attribute = "$${meta.workload_type_portal}"
    value     = "true"
  }
]

network = {
  mode = "host"
  ports = {
    "grpc" = {
      check_type = "grpc"
    }
    "admin" = {
      check_type = "http"
      check_path = "/healthcheck"
    }
    "debug" = {
      check_type = "tcp"
    }
  }
}

env_vars = [
  {
    key   = "DISABLE_STATS"
    value = "true"
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
    key   = "GRPC_PORT"
    value = "$${NOMAD_PORT_grpc}"
  },
  {
    key   = "PORT",
    value = "$${NOMAD_PORT_admin}"
  },
  {
    key = "DEBUG_PORT"
    value = "$${NOMAD_PORT_debug}"
  },
  {
    key   = "LOG_LEVEL",
    value = "info"
  },
  {
    key = "FORCE_START_WITHOUT_INITIAL_CONFIG"
    value = "false"
  },
  {
    key = "CONFIG_TYPE"
    value = "GRPC_XDS_SOTW"
  },
  {
    key = "CONFIG_GRPC_XDS_NODE_ID"
    value = "controlplane-ratelimit"
  },
  {
    key = "CONFIG_GRPC_XDS_SERVER_URL"
    value = "localhost:9599"
  },
  {
    key = "OTEL_EXPORTER_OTLP_ENDPOINT"
    value = "http://127.0.0.1:26000"
  },
  {
    key = "TRACING_EXPORTER_PROTOCOL"
    value = "grpc"
  },
  {
    key = "TRACING_SERVICE_NAME"
    value = "controlplane-ratelimit"
  },
  {
    key = "TRACING_SERVICE_INSTANCE_ID"
    value = "$${NOMAD_ALLOC_ID}"
  }
]

env_secrets = [
  {
    source = "kt_secrets::redis_api_general_master_password"
    dest   = "REDIS_AUTH"
  }
]

args = [
  "/usr/bin/ratelimit-server"
]
