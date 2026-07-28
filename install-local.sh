#!/bin/sh
# Build iris from the working tree, package it like a release asset, and run
# the repo's own install.sh against it (IRIS_BASE_URL=file://). 1:1 with
# `curl -fsSL https://install.iris-lakehouse.bymarreco.com/snapshot | bash`,
# local bits. Extra knobs pass through: IRIS_DEST, IRIS_ENGINE_SETUP, NO_COLOR.
#
# A GitHub-hosted private catalog needs a credential, so when the GitHub CLI is
# logged in this borrows its token for raw.githubusercontent.com and hands it to
# the installer as IRIS_CATALOG_TOKENS. Setup fetches every catalog it is asked
# to record and fails the install when one does not answer, so a missing or
# unusable token stops here rather than surfacing later as an engine that comes
# up healthy and lists no packs. An already-set IRIS_CATALOG_TOKENS wins.
#
# Same-shell: after install, `iris` should work without hash -r when the primary
# shim (~/.local/bin) is on PATH, or when a passwordless-sudo refresh of
# /usr/local/bin/iris covers a stale bash hash from older installs.
#   sh install-local.sh && iris --version
#   sh install-local.sh --default   # clean dev loop: wipe existing state, local engine, public catalog
#   sh install-local.sh && iris uninstall --yes
set -eu

ROOT="$(git rev-parse --show-toplevel)"
DEV="${ROOT}/.local"
mkdir -p "$DEV"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) echo "install-local: unsupported architecture: $arch" >&2; exit 1 ;;
esac

VERSION="local.$(date +%Y%m%d).$(git -C "$ROOT" rev-parse --short=12 HEAD)$(git -C "$ROOT" diff --quiet || echo -dirty)"
echo "· Building ${VERSION} (${os}/${arch})"
CGO_ENABLED=0 go build -trimpath \
  -ldflags="-s -w -X github.com/MateusAMP2119/iris-lakehouse/internal/buildinfo.Version=${VERSION}" \
  -o "$DEV/iris" "${ROOT}/cmd/iris"

tar -czf "${DEV}/iris_${os}_${arch}.tar.gz" -C "$DEV" iris
(cd "$DEV" && shasum -a 256 "iris_${os}_${arch}.tar.gz" > checksums.txt)

# Borrow the GitHub CLI's token for a private catalog. Absence is not fatal here:
# a public catalog needs none, and setup's fetch is what decides. The token is
# never echoed -- it goes straight into the environment install.sh inherits.
if [ -z "${IRIS_CATALOG_TOKENS:-}" ] && command -v gh >/dev/null 2>&1; then
  if gh_token=$(gh auth token 2>/dev/null) && [ -n "$gh_token" ]; then
    IRIS_CATALOG_TOKENS="raw.githubusercontent.com=${gh_token}"
    export IRIS_CATALOG_TOKENS
    echo "· Using your GitHub CLI login for raw.githubusercontent.com"
  fi
fi

exec env IRIS_BASE_URL="file://${DEV}" bash "${ROOT}/install.sh" "$@"
