#!/usr/bin/env bash
# Verifies that a release archive can be loaded and operated without an
# external network. The PostgreSQL image must already exist locally; CI pulls
# it before this script creates an --internal Docker network.
set -Eeuo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 moyro:v1.2.3 moyro-v1.2.3.tar.gz" >&2
  exit 2
fi

image="$1"
archive="$2"
if [[ ! "${image}" =~ ^moyro:v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
  echo "invalid image tag: ${image}" >&2
  exit 2
fi
version="${image#moyro:}"
expected_archive="moyro-${version}.tar.gz"
if [[ "$(basename "${archive}")" != "${expected_archive}" ]]; then
  echo "archive must be named ${expected_archive}" >&2
  exit 2
fi
if [[ ! -s "${archive}" ]]; then
  echo "archive is missing or empty: ${archive}" >&2
  exit 2
fi

archive_manifest="$(gzip -dc "${archive}" | tar -xOf - manifest.json)"
archive_config_path="$(printf '%s' "${archive_manifest}" | sed -n 's/^\[{"Config":"\([^"]*\)".*/\1/p')"
archive_repo_tags="$(printf '%s' "${archive_manifest}" | sed -n 's/.*"RepoTags":\[\([^]]*\)\].*/\1/p')"
archive_config_fields="$(printf '%s' "${archive_manifest}" | awk '{count += gsub(/"Config":/, "&")} END {print count + 0}')"
archive_tag_fields="$(printf '%s' "${archive_manifest}" | awk '{count += gsub(/"RepoTags":/, "&")} END {print count + 0}')"

config_digest() {
  local config_path="$1"
  if [[ "${config_path}" =~ ^([0-9a-f]{64})\.json$ ]]; then
    printf '%s' "${BASH_REMATCH[1]}"
    return 0
  fi
  if [[ "${config_path}" =~ ^blobs/sha256/([0-9a-f]{64})$ ]]; then
    printf '%s' "${BASH_REMATCH[1]}"
    return 0
  fi
  return 1
}

archive_config_digest="$(config_digest "${archive_config_path}" || true)"
if [[ "${archive_config_fields}" != "1" || "${archive_tag_fields}" != "1" ||
      -z "${archive_config_digest}" ||
      "${archive_repo_tags}" != "\"${image}\"" ]]; then
  echo "archive must contain exactly the ${image} image reference" >&2
  exit 1
fi
docker load --input "${archive}" >/dev/null
loaded_manifest="$(docker save "${image}" | tar -xOf - manifest.json)"
loaded_config_path="$(printf '%s' "${loaded_manifest}" | sed -n 's/^\[{"Config":"\([^"]*\)".*/\1/p')"
loaded_config_digest="$(config_digest "${loaded_config_path}" || true)"
if [[ -z "${loaded_config_digest}" || "${loaded_config_digest}" != "${archive_config_digest}" ]]; then
  echo "loaded image config ${loaded_config_path:-<empty>} does not match archive config ${archive_config_path}" >&2
  exit 1
fi
image_platform="$(docker image inspect --format '{{.Os}}/{{.Architecture}}' "${image}")"
if [[ "${image_platform}" != "linux/amd64" ]]; then
  echo "image platform ${image_platform} is not linux/amd64" >&2
  exit 1
fi
label_title="$(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.title" }}' "${image}")"
if [[ "${label_title}" != "moyro" ]]; then
  echo "image title label ${label_title} is not moyro" >&2
  exit 1
fi
label_version="$(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.version" }}' "${image}")"
if [[ "${label_version}" != "${version}" ]]; then
  echo "image label version ${label_version} does not match ${version}" >&2
  exit 1
fi
label_revision="$(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "${image}")"
if [[ -n "${EXPECTED_COMMIT:-}" && "${label_revision}" != "${EXPECTED_COMMIT}" ]]; then
  echo "image revision ${label_revision} does not match expected commit ${EXPECTED_COMMIT}" >&2
  exit 1
fi
image_user="$(docker image inspect --format '{{.Config.User}}' "${image}")"
if [[ "${image_user}" != "65532:65532" ]]; then
  echo "image user ${image_user:-<empty>} is not the non-root release identity 65532:65532" >&2
  exit 1
fi

postgres_image="${POSTGRES_IMAGE:-postgres:16-alpine@sha256:cf78e76683b9ca8c5733cbbdce6c9262b45b6767934dd0a95e671f9a0fc20685}"
if ! docker image inspect "${postgres_image}" >/dev/null 2>&1; then
  echo "preload ${postgres_image} before running the offline verification" >&2
  exit 1
fi

suffix="${GITHUB_RUN_ID:-local}-$$"
network="moyro-release-${suffix}"
postgres_container="moyro-postgres-${suffix}"
app_container="moyro-app-${suffix}"
data_volume="moyro-data-${suffix}"

cleanup() {
  docker container rm --force "${app_container}" "${postgres_container}" >/dev/null 2>&1 || true
  docker network rm "${network}" >/dev/null 2>&1 || true
  docker volume rm "${data_volume}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker network create --internal "${network}" >/dev/null
docker volume create "${data_volume}" >/dev/null
docker run --detach --name "${postgres_container}" --network "${network}" \
  --env POSTGRES_USER=moyro \
  --env POSTGRES_PASSWORD=moyro-release-test \
  --env POSTGRES_DB=moyro \
  "${postgres_image}" >/dev/null

postgres_ready=false
for _ in $(seq 1 60); do
  # pg_isready can report success while crash recovery still rejects normal
  # sessions. An authenticated query proves migrations can connect reliably.
  if docker exec "${postgres_container}" psql -X -qAt \
    --username moyro --dbname moyro --command 'SELECT 1' >/dev/null 2>&1; then
    postgres_ready=true
    break
  fi
  sleep 1
done
if [[ "${postgres_ready}" != true ]]; then
  echo "PostgreSQL did not become ready" >&2
  exit 1
fi
if ! docker exec "${postgres_container}" /bin/sh -c 'command -v wget >/dev/null'; then
  echo "${postgres_image} must provide BusyBox-compatible wget for internal HTTP probes" >&2
  exit 1
fi

bootstrap_password='MoyroRelease!2026'
encryption_key='bW95cm8tcmVsZWFzZS10ZXN0LWtleS0zMmJ5dGUhISE='
docker run --detach --name "${app_container}" --network "${network}" \
  --read-only --tmpfs /tmp:rw,noexec,nosuid,size=64m \
  --cap-drop ALL --security-opt no-new-privileges \
  --mount "type=volume,src=${data_volume},dst=/var/lib/moyro" \
  --env "POSTGRES_DSN=postgres://moyro:moyro-release-test@${postgres_container}:5432/moyro?sslmode=disable" \
  --env BOOTSTRAP_ADMIN=admin@moyro.local \
  --env "BOOTSTRAP_ADMIN_PASSWORD=${bootstrap_password}" \
  --env "ENCRYPTION_KEY=${encryption_key}" \
  "${image}" >/dev/null

# Docker intentionally does not expose host ports from an --internal network.
# The PostgreSQL Alpine image already contains BusyBox wget, so it doubles as
# the HTTP probe without adding a third runtime image or giving moyro egress.
base_url="http://${app_container}:8065"

probe_get() {
  docker exec "${postgres_container}" wget -q -T 3 -Y off -O - "$1"
}

wait_for_health() {
  for _ in $(seq 1 60); do
    if probe_get "${base_url}/healthz" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  return 1
}

if ! wait_for_health; then
  echo "moyro did not become healthy" >&2
  docker logs "${app_container}" >&2 || true
  exit 1
fi

probe_get "${base_url}/" | grep -F '<title>moyro</title>' >/dev/null
probe_get "${base_url}/api/v4/config/client" | grep -F "\"Version\":\"${version}\"" >/dev/null

login() {
  docker exec "${postgres_container}" wget -q -T 10 -Y off -O - \
    --header 'Content-Type: application/json' \
    --post-data "{\"login_id\":\"admin@moyro.local\",\"password\":\"${bootstrap_password}\"}" \
    "${base_url}/api/v4/users/login" >/dev/null
}

login
docker restart "${app_container}" >/dev/null
if ! wait_for_health; then
  echo "moyro did not recover after restart" >&2
  exit 1
fi
login

configured_names="$(docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "${app_container}" |
  cut -d= -f1 | sed '/^$/d' | sort)"
expected_names="$(printf '%s\n' BOOTSTRAP_ADMIN BOOTSTRAP_ADMIN_PASSWORD ENCRYPTION_KEY PATH POSTGRES_DSN SSL_CERT_FILE | sort)"
if [[ "${configured_names}" != "${expected_names}" ]]; then
  echo "container environment is not exactly the four application inputs plus the two fixed runtime variables" >&2
  echo "configured names:" >&2
  printf '%s\n' "${configured_names}" >&2
  echo "expected names:" >&2
  printf '%s\n' "${expected_names}" >&2
  exit 1
fi

echo "verified ${archive}: offline startup, web UI, version, bootstrap login, and restart"
