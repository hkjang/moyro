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
#
# Every file a split produced is tracked too. Without that, splitting a file
# only moves the concentration into an unwatched sibling and the ratchet stops
# meaning anything. Headroom is deliberately small — roughly one feature's
# worth — so the next large addition has to choose a seam rather than pile on.

# Server: the compatibility waves were carved out of handlers.go and
# compat_wave_handlers.go along their existing wave banners.
check_max_bytes "server/internal/httpapi/handlers.go" 130000
check_max_bytes "server/internal/httpapi/compat_wave_handlers.go" 64000
check_max_bytes "server/internal/httpapi/compat_wave_handlers_early.go" 58000
check_max_bytes "server/internal/httpapi/compat_wave_handlers_late.go" 61000
check_max_bytes "server/internal/httpapi/compat_wave_handlers_final.go" 77000

# Web: the workspace container, the admin panel, and the stylesheet.
check_max_bytes "webapp/src/components/ChatView.tsx" 78000
check_max_bytes "webapp/src/components/IntegrationsPanel.tsx" 81000
check_max_bytes "webapp/src/index.css" 30000
check_max_bytes "webapp/src/styles/admin.css" 25000
check_max_bytes "webapp/src/styles/features.css" 20000
check_max_bytes "webapp/src/styles/workspace-chrome.css" 44000

# Web API surface. client.ts is now only a re-export barrel, so its limit is
# tight on purpose: new endpoints belong in the module that owns the surface.
check_max_bytes "webapp/src/api/client.ts" 2000
check_max_bytes "webapp/src/api/chat.ts" 31000
check_max_bytes "webapp/src/api/integrations.ts" 18000
check_max_bytes "webapp/src/api/compat.ts" 18000
check_max_bytes "webapp/src/api/moyro.ts" 18000

exit "${failed}"
