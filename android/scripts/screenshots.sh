#!/bin/sh
# Screenshots of the four screens, taken on an emulator in CI. The screens that need an account are drawn from the
# debug-only PreviewActivity with sample data; the Pair screen is the real one.
#
#   scripts/screenshots.sh [outdir]
set -eu

out=${1:-shots}
mkdir -p "$out"

adb wait-for-device
adb shell wm dismiss-keyguard || true
adb shell settings put global window_animation_scale 0 || true
adb shell settings put global transition_animation_scale 0 || true
adb shell settings put global animator_duration_scale 0 || true

apk=$(find android/app/build/outputs/apk/debug -name "*.apk" | head -n 1)
adb install -r "$apk"

for screen in pair approvals activity phone; do
    adb shell am force-stop network.openvibe.link || true
    adb shell am start -W -n network.openvibe.link/.PreviewActivity --es screen "$screen" >/dev/null
    sleep 2
    adb exec-out screencap -p > "$out/$screen.png"
    echo "shot $screen"
done

# The launcher entry, as a person sees it first.
adb shell am force-stop network.openvibe.link || true
adb shell am start -W -n network.openvibe.link/.MainActivity >/dev/null
sleep 2
adb exec-out screencap -p > "$out/app.png"
echo "shot app"
