#!/bin/sh
# Tests that install.sh pip-installs the bundled plugin behind every option that turns one on, so the Link never starts
# a plugin whose package is missing ("No module named openvibe_ai": --ai configured the ai plugin and never installed
# it, 2026-11-12). Community drivers (looi) come from their own registry and are not in this list.
set -eu

here="$(cd "$(dirname "$0")" && pwd)"
installer="$here/../install/install.sh"
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

# option → the bundled plugin it needs
for pair in "--ai:ai" "--computer:computer" "--desk-host:computer" "--driver adeept:adeept_adr036" "--driver cozmo:cozmo" "--driver ros2:ros2" "base:sdk" "base:dryrun" "base:relay"; do
	plugin="${pair##*:}"
	grep -Eq "PIP\"? install [^#]*plugins/$plugin([\"\[ ]|$)" "$installer" || fail "${pair%%:*} needs plugins/$plugin, which the installer never pip-installs"
done
printf 'ok: every option installs the bundled plugin it turns on\n'
