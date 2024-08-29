env_vars = [
  {
    key = "TRACING_ENABLED"
    value = "true"
  },
  {
    key = "TRACING_SERVICE_NAME"
    value = "controlplane-ratelimit"
  },
  {
    key = "TRACING_SERVICE_INSTANCE_ID"
    value = "$${NOMAD_ALLOC_ID}"
  },
]
