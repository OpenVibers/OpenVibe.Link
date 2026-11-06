#!/bin/sh
# The latency viewer's dependencies in a cache venv, then the viewer. See scripts/stream-viewer.py.
set -eu

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cache=${XDG_CACHE_HOME:-$HOME/.cache}/openvibe-stream-viewer
venv=$cache/venv

if [ ! -x "$venv/bin/python" ]; then
    command -v python3 >/dev/null || { echo "stream-viewer: python3 is required" >&2; exit 2; }
    mkdir -p "$cache"
    python3 -m venv "$venv" || {
        echo "stream-viewer: could not create a venv under $cache; install python3-venv" >&2
        exit 2
    }
fi

if ! "$venv/bin/python" -c 'import aiortc, aiohttp, cv2, numpy' >/dev/null 2>&1; then
    echo "stream-viewer: installing aiortc, aiohttp and OpenCV once into $venv" >&2
    "$venv/bin/python" -m pip install --quiet --disable-pip-version-check aiortc aiohttp opencv-python-headless numpy || {
        echo "stream-viewer: pip install failed (no network?)" >&2
        exit 2
    }
fi

exec "$venv/bin/python" "$here/stream-viewer.py" "$@"
