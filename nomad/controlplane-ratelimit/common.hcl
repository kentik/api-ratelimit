job_name  = "controlplane-ratelimit"
app_count = 2


constraints = [
  {
    attribute = "$${meta.run_envoy-portal}"
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
  // Stats go as DogStatsD over UDP to the host's Telegraf statsd input (kt_defaults::telegraf, port 8190), which
  // writes them to VictoriaMetrics as <name>_value with the tags as labels. USE_STATSD must be off: the runner
  // refuses to start with both sinks on. (With both off, stats went to stdout, which is why #12 disabled them.)
  {
    key   = "USE_DOG_STATSD"
    value = "true"
  },
  {
    key   = "USE_STATSD"
    value = "false"
  },
  {
    key   = "STATSD_HOST"
    value = "127.0.0.1"
  },
  {
    key   = "STATSD_PORT"
    value = "8190"
  },
  // Mogrifiers turn ratelimit.service.rate_limit.<domain>.<descriptor path>.<stat> into one metric per stat with
  // domain/descriptor tags. The first match wins. Per-company overrides (cid_<id>) are folded into
  // descriptor=cid_override.<rest>, so no metric carries a company id; per-company questions go to traces.
  {
    key   = "USE_DOG_STATSD_MOGRIFIERS"
    value = "OVERRIDE_UTILIZATION,UTILIZATION,OVERRIDE,DESCRIPTOR"
  },
  {
    key   = "DOG_STATSD_MOGRIFIER_OVERRIDE_UTILIZATION_PATTERN"
    value = "^ratelimit\\.service\\.rate_limit\\.([^.]+)\\.cid_[0-9]+\\.(.*)\\.utilization_le_([0-9]+)$"
  },
  {
    key   = "DOG_STATSD_MOGRIFIER_OVERRIDE_UTILIZATION_NAME"
    value = "ratelimit.service.rate_limit.utilization"
  },
  {
    key   = "DOG_STATSD_MOGRIFIER_OVERRIDE_UTILIZATION_TAGS"
    value = "domain:$1,descriptor:cid_override.$2,utilization_le:$3"
  },
  {
    key   = "DOG_STATSD_MOGRIFIER_UTILIZATION_PATTERN"
    value = "^ratelimit\\.service\\.rate_limit\\.([^.]+)\\.(.*)\\.utilization_le_([0-9]+)$"
  },
  {
    key   = "DOG_STATSD_MOGRIFIER_UTILIZATION_NAME"
    value = "ratelimit.service.rate_limit.utilization"
  },
  {
    key   = "DOG_STATSD_MOGRIFIER_UTILIZATION_TAGS"
    value = "domain:$1,descriptor:$2,utilization_le:$3"
  },
  {
    key   = "DOG_STATSD_MOGRIFIER_OVERRIDE_PATTERN"
    value = "^ratelimit\\.service\\.rate_limit\\.([^.]+)\\.cid_[0-9]+\\.(.*)\\.([^.]+)$"
  },
  {
    key   = "DOG_STATSD_MOGRIFIER_OVERRIDE_NAME"
    value = "ratelimit.service.rate_limit.$3"
  },
  {
    key   = "DOG_STATSD_MOGRIFIER_OVERRIDE_TAGS"
    value = "domain:$1,descriptor:cid_override.$2"
  },
  {
    key   = "DOG_STATSD_MOGRIFIER_DESCRIPTOR_PATTERN"
    value = "^ratelimit\\.service\\.rate_limit\\.([^.]+)\\.(.*)\\.([^.]+)$"
  },
  {
    key   = "DOG_STATSD_MOGRIFIER_DESCRIPTOR_NAME"
    value = "ratelimit.service.rate_limit.$3"
  },
  {
    key   = "DOG_STATSD_MOGRIFIER_DESCRIPTOR_TAGS"
    value = "domain:$1,descriptor:$2"
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
