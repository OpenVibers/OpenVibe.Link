#!/bin/sh
# Build the release artifacts into dist/: one binary per platform, the plugin bundle, install.sh and SHA256SUMS.
#   scripts/dist.sh [VERSION]
set -eu
VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
cd "$(dirname "$0")/.."
rm -rf dist
mkdir -p dist
for target in linux/amd64 linux/arm64 linux/arm/7 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
	os="${target%%/*}"
	rest="${target#*/}"
	arch="${rest%%/*}"
	arm=""
	name="openvibe-link-$os-$arch"
	if [ "$arch" = arm ]; then
		arm="${rest#*/}"
		name="openvibe-link-$os-armv$arm"
	fi
	[ "$os" = windows ] && name="$name.exe"
	CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" GOARM="$arm" \
		go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "dist/$name" ./cmd/openvibe-node
	# openvibe-node is kept as an alias for existing installs and scripts (plan 2026-11-07 §5): the same binary,
	# under the old asset name, in every release for a year. The daemon's unit/service name does not change.
	old="openvibe-node-${name#openvibe-link-}"
	cp "dist/$name" "dist/$old"
	echo "built dist/$name and dist/$old"
done
# The plugin bundle: the Python packages without their tests.
tar -czf dist/openvibe-node-plugins.tar.gz -C plugins \
	--exclude=tests --exclude=__pycache__ --exclude='*.egg-info' --exclude=.pytest_cache --exclude=build \
	sdk dryrun adeept_adr036 cozmo relay ros2 computer voice ai
# OpenVibe.Bot's GET /install route and BOT_INSTALLER_SOURCE_URL are documented to serve this asset name:
# https://github.com/OpenVibers/OpenVibe.Node/releases/latest/download/install.sh — do not rename it.
cp install/install.sh dist/install.sh
# Friendly Linux and macOS artifacts: .deb, .rpm and a tarball per architecture, from the binaries above.
scripts/package.sh "$VERSION"
(cd dist && if command -v sha256sum >/dev/null 2>&1; then sha256sum -- * >SHA256SUMS; else shasum -a 256 -- * >SHA256SUMS; fi)
echo "dist/ ready for $VERSION"
