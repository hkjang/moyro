#!/usr/bin/env bash
set -euo pipefail

# Download the exact public Mattermost plugin bundles exercised by CI.  The
# checksums make the compatibility test reproducible even if a release asset is
# replaced upstream. EchoSummary 0.6.5 is additionally exercised from the local
# sibling checkout during release preparation; 0.6.4 is the latest published
# asset available to GitHub-hosted runners.

destination="${1:-}"
if [[ -z "${destination}" ]]; then
  echo "usage: $0 DESTINATION" >&2
  exit 2
fi
mkdir -p "${destination}"

download() {
  local repository="$1"
  local tag="$2"
  local asset="$3"
  local expected="$4"
  local target="${destination}/${asset}"

  curl --fail --location --silent --show-error \
    --retry 3 --retry-all-errors \
    "https://github.com/${repository}/releases/download/${tag}/${asset}" \
    --output "${target}"

  local actual
  actual="$(sha256sum "${target}" | awk '{print $1}')"
  if [[ "${actual}" != "${expected}" ]]; then
    echo "checksum mismatch for ${asset}: got ${actual}, want ${expected}" >&2
    exit 1
  fi
  printf '%s  %s\n' "${actual}" "${asset}"
}

download \
  "hkjang/mattermost-botman-plugin" "v0.1.2" \
  "com.mattermost.botman-0.1.2.tar.gz" \
  "08235815800e4a5305d6f3bf940ffc01ff026c224889ad7872990223cf464e92"
download \
  "hkjang/mattermost-chatdump-plugin" "v0.5.1" \
  "com.hkjang.mattermost-chatdump-plugin-0.5.1.tar.gz" \
  "c8092669b016d2d574d95d8424e566019dc145917eeeafd5d89eaff901353937"
download \
  "hkjang/mattermost-echosummary-plugin" "v0.6.4" \
  "com.mattermost.echosummary-0.6.4.tar.gz" \
  "f4bc04a2d97c4baa23e2de1340b0781aa740d7fd213465cf06119b0a1f48f8c6"
download \
  "hkjang/mattermost-langflow-plugin" "v0.1.20" \
  "com.mattermost.langflow-0.1.20.tar.gz" \
  "2792c4da9acd78376d01ea83dc3a5159e33a3a55b47ed60b56323712d713783e"
