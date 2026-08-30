#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 2 ]]; then
  echo "usage: $0 <image> <capture-directory> [playwright-spec ...]" >&2
  exit 2
fi

image="$1"
capture_dir="$2"
shift 2
if [[ $# -gt 0 ]]; then
  playwright_specs=("$@")
else
  playwright_specs=(e2e/product-pages.spec.ts e2e/plugin-compatibility.spec.ts)
fi
port="${MOYRO_E2E_PORT:-18065}"
suffix="${GITHUB_RUN_ID:-local}-$$"
network="moyro-e2e-net-${suffix}"
db_container="moyro-e2e-db-${suffix}"
app_container="moyro-e2e-app-${suffix}"
base_url="http://127.0.0.1:${port}"
expected_version="${MOYRO_EXPECTED_VERSION:-${image##*:}}"
admin_email="admin@moyro.local"
admin_password="MoyroRelease!2026"
encryption_key="MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
postgres_image="${POSTGRES_IMAGE:-postgres:16-alpine@sha256:cf78e76683b9ca8c5733cbbdce6c9262b45b6767934dd0a95e671f9a0fc20685}"

cleanup() {
  status=$?
  if [[ $status -ne 0 ]]; then
    docker logs --tail 200 "${app_container}" 2>/dev/null || true
    docker logs --tail 100 "${db_container}" 2>/dev/null || true
  fi
  docker rm -f "${app_container}" "${db_container}" >/dev/null 2>&1 || true
  docker network rm "${network}" >/dev/null 2>&1 || true
  exit $status
}
trap cleanup EXIT INT TERM

mkdir -p "${capture_dir}"
capture_dir="$(cd "${capture_dir}" && pwd -P)"
# Browser tests run on the host and need the published loopback port. The
# separate release verifier performs the strict internal-only/offline check.
docker network create "${network}" >/dev/null
docker run -d --name "${db_container}" --network "${network}" \
  -e POSTGRES_USER=moyro \
  -e POSTGRES_PASSWORD=moyro-e2e-db-password \
  -e POSTGRES_DB=moyro \
  --tmpfs /var/lib/postgresql/data:rw,nosuid,nodev,size=512m \
  "${postgres_image}" >/dev/null

for _ in $(seq 1 120); do
  if docker exec "${db_container}" psql -U moyro -d moyro -v ON_ERROR_STOP=1 -c "SELECT 1" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
docker exec "${db_container}" psql -U moyro -d moyro -v ON_ERROR_STOP=1 -c "SELECT 1" >/dev/null

docker run -d --name "${app_container}" --network "${network}" \
  -p "127.0.0.1:${port}:8065" \
  -e "POSTGRES_DSN=postgres://moyro:moyro-e2e-db-password@${db_container}:5432/moyro?sslmode=disable" \
  -e "BOOTSTRAP_ADMIN=${admin_email}" \
  -e "BOOTSTRAP_ADMIN_PASSWORD=${admin_password}" \
  -e "ENCRYPTION_KEY=${encryption_key}" \
  --tmpfs /var/lib/moyro:rw,nosuid,nodev,exec,uid=65532,gid=65532,mode=0755,size=2g \
  "${image}" >/dev/null

for _ in $(seq 1 120); do
  if curl --fail --silent --show-error "${base_url}/api/v4/system/ping" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
curl --fail --silent --show-error "${base_url}/api/v4/system/ping" >/dev/null

# Keep the compact Mattermost v4 smoke contract in the same image-backed gate
# as the browser suite. The script provisions its user through a single-use
# invite, matching the release default that disables anonymous registration.
BASE="${base_url}" \
ADMIN_LOGIN="${admin_email}" \
ADMIN_PASSWORD="${admin_password}" \
bash scripts/contract-test.sh

(
  cd webapp
  MOYRO_BASE_URL="${base_url}" \
  MOYRO_ADMIN="${admin_email}" \
  MOYRO_ADMIN_PASSWORD="${admin_password}" \
  MOYRO_CAPTURE_DIR="${capture_dir}" \
  MOYRO_EXPECTED_VERSION="${expected_version}" \
  npx playwright test "${playwright_specs[@]}" --project=chromium
)
