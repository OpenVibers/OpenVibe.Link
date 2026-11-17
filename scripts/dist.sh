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
		go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "dist/$name" ./cmd/openvibe-link
	echo "built dist/$name"
done
# ov-desk (desk/, Rust): the lent desktop's helper on Linux, the computer plugin's first choice (2026-11-17: the race in
# docs/computer.md). Built where cargo is; a release (OV_DESK_REQUIRED=1) must have it.
if command -v cargo >/dev/null 2>&1; then
	for pair in amd64:x86_64-unknown-linux-gnu arm64:aarch64-unknown-linux-gnu; do
		arch="${pair%%:*}"
		triple="${pair#*:}"
		if [ "$arch" = arm64 ] && [ "$(uname -m)" != aarch64 ]; then
			command -v aarch64-linux-gnu-gcc >/dev/null 2>&1 || { [ "${OV_DESK_REQUIRED:-0}" = 1 ] && { echo "dist: aarch64-linux-gnu-gcc is needed for ov-desk arm64" >&2; exit 1; }; echo "dist: no aarch64 linker, ov-desk arm64 skipped"; continue; }
			export CARGO_TARGET_AARCH64_UNKNOWN_LINUX_GNU_LINKER=aarch64-linux-gnu-gcc
		fi
		cargo build --quiet --release --locked --manifest-path desk/Cargo.toml --target "$triple"
		cp "desk/target/$triple/release/ov-desk" "dist/ov-desk-linux-$arch"
		echo "built dist/ov-desk-linux-$arch"
	done
elif [ "${OV_DESK_REQUIRED:-0}" = 1 ]; then
	echo "dist: cargo is needed for ov-desk" >&2
	exit 1
else
	echo "dist: no cargo, ov-desk skipped"
fi
# The plugin bundle: the Python packages without their tests.
tar -czf dist/openvibe-link-plugins.tar.gz -C plugins \
	--exclude=tests --exclude=__pycache__ --exclude='*.egg-info' --exclude=.pytest_cache --exclude=build \
	sdk dryrun adeept_adr036 cozmo relay ros2 computer voice ai
# OpenVibe.Bot's GET /install route and BOT_INSTALLER_SOURCE_URL are documented to serve this asset name:
# https://github.com/OpenVibers/OpenVibe.Link/releases/latest/download/install.sh — do not rename it.
cp install/install.sh dist/install.sh
# Friendly Linux and macOS artifacts: .deb, .rpm and a tarball per architecture, from the binaries above.
scripts/package.sh "$VERSION"
(cd dist && if command -v sha256sum >/dev/null 2>&1; then sha256sum -- * >SHA256SUMS; else shasum -a 256 -- * >SHA256SUMS; fi)
echo "dist/ ready for $VERSION"
