#!/bin/sh
# Generate the phone's design system from the pinned openvibe-shared release, so the app wears the same theme and kit
# as every OpenVibe site. Run it after a Design release; never edit the generated files.
#
#   scripts/gen-design.sh          # rewrite app/src/main/java/network/openvibe/{theme,kit}
#   scripts/gen-design.sh --check  # refuse if the generated files differ from what is committed
#
# It downloads the release tarball (no npm registry, no token), runs the release's own scripts/native-check.js, and
# copies exactly the Kotlin files it writes. The pinned version is android/design-version.txt.
set -eu

unset CDPATH
android=$(cd -- "$(dirname -- "$0")/.." && pwd)
version=$(tr -d ' \n' < "$android/design-version.txt")
target="$android/app/src/main/java/network/openvibe"
check=no
[ "${1:-}" = "--check" ] && check=yes

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "openvibe-shared $version"
curl -fsSL "https://codeload.github.com/OpenVibers/OpenVibe.Shared/tar.gz/refs/tags/$version" | tar -xz -C "$tmp"
src=$(find "$tmp" -maxdepth 1 -type d -name 'OpenVibe.Shared-*' | head -n 1)
[ -n "$src" ] || { echo "the release tarball had no OpenVibe.Shared-* directory" >&2; exit 1; }

node "$src/scripts/native-check.js" --out "$tmp/native"

mkdir -p "$target/theme" "$target/kit"
cp "$tmp/native/kotlin/VibeTheme.kt" "$target/theme/VibeTheme.kt"
cp "$tmp/native/kotlin/OvKit.kt" "$target/kit/OvKit.kt"
cp "$tmp/native/kotlin/OvKitStubs.kt" "$target/kit/OvKitStubs.kt"
cp "$tmp/native/kotlin/KitRenderer.kt" "$target/kit/KitRenderer.kt"

echo "wrote $target/theme/VibeTheme.kt and $target/kit/{OvKit,OvKitStubs,KitRenderer}.kt"

if [ "$check" = yes ]; then
    if ! git -C "$android" diff --quiet -- app/src/main/java/network/openvibe; then
        echo "the generated design files are stale: run scripts/gen-design.sh and commit the result" >&2
        git -C "$android" --no-pager diff --stat -- app/src/main/java/network/openvibe >&2
        exit 1
    fi
    echo "the generated design files match $version"
fi
