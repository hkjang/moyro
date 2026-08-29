#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
failed=0

check_max_bytes() {
  local relative_path="$1"
  local max_bytes="$2"
  local absolute_path="${repo_root}/${relative_path}"

  if [[ ! -f "${absolute_path}" ]]; then
    echo "source-size: missing tracked file: ${relative_path}" >&2
    failed=1
    return
  fi

  local actual_bytes
  actual_bytes="$(wc -c < "${absolute_path}")"
  if (( actual_bytes > max_bytes )); then
    echo "source-size: ${relative_path} is ${actual_bytes} bytes; limit is ${max_bytes}. Split the feature before adding more code." >&2
    failed=1
  else
    echo "source-size: ${relative_path} ${actual_bytes}/${max_bytes} bytes"
  fi
}

# These are ratchets, not aspirational limits. Lower them whenever a module is
# split so CI prevents the large-file concentration from returning.
check_max_bytes "server/internal/httpapi/handlers.go" 180000
check_max_bytes "server/internal/httpapi/compat_wave_handlers.go" 190000
check_max_bytes "webapp/src/components/ChatView.tsx" 185000
check_max_bytes "webapp/src/index.css" 98000
check_max_bytes "webapp/src/components/IntegrationsPanel.tsx" 84000
check_max_bytes "webapp/src/api/client.ts" 64000

exit "${failed}"
