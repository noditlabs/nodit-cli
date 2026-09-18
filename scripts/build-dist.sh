#!/usr/bin/env bash
#
# The three endpoints are injected at build time and have no runtime override, so a
# missing one fails here rather than producing a binary that exits with INVALID_BUILD
# on first use.
#
# Usage: scripts/build-dist.sh <version>

set -euo pipefail

VERSION="${1:?version is required}"

: "${AUTH_ISSUER:?AUTH_ISSUER is required}"
: "${API_RESOURCE:?API_RESOURCE is required}"
: "${PRODUCT_DOMAIN:?PRODUCT_DOMAIN is required}"

# go.mod pins the version; auto lets it be fetched when the local default is local.
export GOTOOLCHAIN="${GOTOOLCHAIN:-auto}"

TARGETS=(linux/amd64 darwin/amd64 darwin/arm64 windows/amd64)

buildinfo="$(go list -m)/internal/buildinfo"
mkdir -p dist

for target in "${TARGETS[@]}"; do
  os="${target%/*}"
  arch="${target#*/}"
  name="nodit-${os}-${arch}"
  binary="nodit"
  if [[ "$os" == "windows" ]]; then
    binary="nodit.exe"
  fi
  mkdir -p "dist/$name"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build \
    -trimpath \
    -ldflags "-s -w -X ${buildinfo}.AuthIssuer=${AUTH_ISSUER} -X ${buildinfo}.APIResource=${API_RESOURCE} -X ${buildinfo}.ProductDomain=${PRODUCT_DOMAIN} -X ${buildinfo}.Version=${VERSION}" \
    -o "dist/$name/$binary" ./cmd/nodit
  cp README.md LICENSE NOTICE "dist/$name/"
  if [[ "$os" == "windows" ]]; then
    (cd dist && zip -qr "${name}.zip" "$name")
  else
    tar -C dist -czf "dist/${name}.tar.gz" "$name"
  fi
done

(cd dist && sha256sum ./*.tar.gz ./*.zip > checksums.txt)
