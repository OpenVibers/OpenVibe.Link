#!/bin/sh
# Bind the Node's Go core (mobile/mobile.go, the pairing and the link) into an Android library the app links against.
# It is the same code the desktop Node runs, so pairing, token refresh and reconnects behave identically.
#
#   scripts/build-core.sh          # write android/app/libs/core.aar
#
# Needs Go, the Android SDK and NDK (ANDROID_HOME or ANDROID_NDK_HOME), and writes nothing into the repository that is
# committed: android/app/libs/ is ignored.
#
# The bind runs against a temporary module that replaces the Node with this checkout, so the Node's own go.mod (and the
# desktop binaries built from it) never gains the bind tool or its newer dependencies.
set -eu

unset CDPATH
android=$(cd -- "$(dirname -- "$0")/.." && pwd)
repo=$(cd -- "$android/.." && pwd)

# Pinned: the bind tool that produced the AAR, so a rebuild is the same build.
GOMOBILE=v0.0.0-20261011034528-197770444867

if [ -z "${ANDROID_NDK_HOME:-}" ]; then
    home=${ANDROID_HOME:-${ANDROID_SDK_ROOT:-}}
    [ -n "$home" ] || { echo "set ANDROID_HOME (or ANDROID_NDK_HOME) to the Android SDK" >&2; exit 1; }
    for candidate in "$home"/ndk/*; do [ -d "$candidate" ] && ndk=$candidate; done
    [ -n "$ndk" ] || { echo "no NDK under $home/ndk: install one with sdkmanager 'ndk;<version>'" >&2; exit 1; }
    ANDROID_NDK_HOME=$ndk
    export ANDROID_NDK_HOME
fi

echo "gomobile $GOMOBILE, NDK $ANDROID_NDK_HOME"
GOBIN=$(go env GOPATH)/bin
GOBIN=$GOBIN go install "golang.org/x/mobile/cmd/gomobile@$GOMOBILE"
GOBIN=$GOBIN go install "golang.org/x/mobile/cmd/gobind@$GOMOBILE"
"$GOBIN/gomobile" init

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
# A module of its own: the Node's go.mod, with a name of its own (a module cannot require itself) and this checkout
# in place of the published module.
sed 's/^module .*/module openvibe-link\/core/' "$repo/go.mod" > "$tmp/go.mod"
cp "$repo/go.sum" "$tmp/"
printf '\nreplace github.com/OpenVibers/OpenVibe.Link => %s\n' "$repo" >> "$tmp/go.mod"

(
    cd "$tmp"
    export GOFLAGS="-mod=mod -buildvcs=false"
    # go get, not go mod tidy: tidy would drop a module the bind uses but nothing imports.
    go get github.com/OpenVibers/OpenVibe.Link@v0.0.0 >/dev/null
    go get -tool "golang.org/x/mobile/cmd/gobind@$GOMOBILE" >/dev/null
    mkdir -p "$android/app/libs"
    "$GOBIN/gomobile" bind -target=android/arm64,android/arm -androidapi 26 \
        -o "$android/app/libs/core.aar" github.com/OpenVibers/OpenVibe.Link/mobile
)

echo "wrote android/app/libs/core.aar"
