# Sourced by the core-stack launcher so the lock survives through its readiness checks.
floatlab_core_start_lock() {
  mkdir -p "$1"
  exec 9>"$1/update.lock"
  flock -n 9
}

floatlab_core_start_inherited_lock() {
  # Only the root mode-switch caller passes this lock across exec. Validate
  # the descriptor before reusing its flock; opening another file is not enough.
  [ "$(readlink /proc/self/fd/9)" = "$1/update.lock" ] || return 1
  flock -n 9
}

floatlab_core_stack_running() {
  # A deliberate mode change must apply its newly selected Compose override.
  [ "${1:-false}" != true ] || return 1
  local service ids
  for service in rqlite victoria-metrics victoria-logs floatlab-control; do
    ids=$(docker ps -q --filter label=com.docker.compose.project=floatlab-system --filter "label=com.docker.compose.service=$service") || return 1
    # Exactly one running container must exist for every management service.
    [ -n "$ids" ] && [[ "$ids" != *$'\n'* ]] || return 1
  done
}
