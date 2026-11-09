#!/bin/sh
# Tests install.sh --plan-logins: the dry run lists the steps and what it found, a missing or signed-out tool is
# skipped with a clear line, only the signed-in tools' login files are named, and a login file's content is never read
# or printed. OPENVIBE_INSTALL_PLAN_LOGINS_DRY_RUN and OPENVIBE_INSTALL_PLAN_LOGINS_CONFIG_ONLY run without root,
# without a service and without touching the real home or config.
set -eu

here="$(cd "$(dirname "$0")" && pwd)"
installer="$here/../install/install.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

home="$tmp/home"
bin="$tmp/bin"
# A clean PATH: the fake tools, then the system's own. The invoking user's PATH may have the real Command Code on it.
path="$bin:/usr/bin:/bin"
mkdir -p "$home/.claude" "$home/.codex" "$home/.local/share/opencode" "$bin"
# The installer needs python3; a clean PATH still has to find it (it may live in /usr/local/bin).
ln -s "$(command -v python3)" "$bin/python3"
for tool in claude codex opencode; do
	printf '#!/bin/sh\nexit 0\n' > "$bin/$tool"
	chmod +x "$bin/$tool"
done
# Claude Code and OpenCode are signed in; Codex is installed but its login file is empty; Command Code is not installed.
printf '%s\n' '{"access_token":"CLAUDE-LOGIN-SECRET"}' > "$home/.claude/.credentials.json"
: > "$home/.codex/auth.json"
printf '%s\n' '{"token":"OPENCODE-LOGIN-SECRET"}' > "$home/.local/share/opencode/auth.json"

dry="$(HOME="$home" PATH="$path" OPENVIBE_INSTALL_PLAN_LOGINS_DRY_RUN=1 sh "$installer" --plan-logins 2>&1)" ||
	fail "the dry run exited non-zero"
check() { printf '%s\n' "$dry" | grep -F -- "$1" >/dev/null || fail "the dry run is missing: $1"; }
check '1. pair this computer'
check '2. set the openvibe-link service'
check '3. name the coding tools'
check '4. write the names'
check 'claude-code: signed in (~/.claude/.credentials.json): named for your jobs'
check 'opencode: signed in (~/.local/share/opencode/auth.json): named for your jobs'
check "codex: installed but not signed in: skipped (run \`codex login\`"
check "command-code: not installed: skipped (install it and sign in with \`cmd login\`"
for secret in CLAUDE-LOGIN-SECRET OPENCODE-LOGIN-SECRET; do
	printf '%s\n' "$dry" | grep -F -- "$secret" >/dev/null && fail "the dry run printed a login's content"
done

# A login file that is there is named even when its CLI is not on this PATH (sudo's secure_path, or a user-local CLI):
# the line says to install that CLI system-wide so a job can run it.
home2="$tmp/home2"
mkdir -p "$home2/.commandcode"
printf '{}\n' > "$home2/.commandcode/auth.json"
dry2="$(HOME="$home2" PATH="$path" OPENVIBE_INSTALL_PLAN_LOGINS_DRY_RUN=1 sh "$installer" --plan-logins 2>&1)" ||
	fail "the second dry run exited non-zero"
printf '%s\n' "$dry2" | grep -F 'command-code: signed in (~/.commandcode/auth.json) but cmd is not on the job runner' >/dev/null ||
	fail "a login without its CLI on PATH was not named with a warning"

conf="$tmp/config.json"
existing="$home/.codex/old.json"
printf '{"server": "https://openvibe.bot", "plugins": [{"name": "dryrun"}], "worker": {"enabled": true, "plan_logins": ["%s"]}}\n' "$existing" > "$conf"
written="$(HOME="$home" PATH="$path" OPENVIBE_INSTALL_PLAN_LOGINS_CONFIG_ONLY="$conf" sh "$installer" --plan-logins 2>&1)" ||
	fail "writing worker.plan_logins exited non-zero"
for secret in CLAUDE-LOGIN-SECRET OPENCODE-LOGIN-SECRET; do
	printf '%s\n' "$written" | grep -F -- "$secret" >/dev/null && fail "the config run printed a login's content"
	grep -F -- "$secret" "$conf" >/dev/null && fail "a login's content reached config.json"
done
python3 - "$conf" "$home" <<'PY' || fail "worker.plan_logins was not written as expected"
import json, os, stat, sys
path, home = sys.argv[1], sys.argv[2]
c = json.load(open(path))
w = c['worker']
assert c['server'] == 'https://openvibe.bot' and c['plugins'] == [{'name': 'dryrun'}], 'the rest of the config stays'
assert w['enabled'] is True, 'the worker section stays'
want = sorted({os.path.join(home, '.codex', 'old.json'),
               os.path.join(home, '.claude', '.credentials.json'),
               os.path.join(home, '.local', 'share', 'opencode', 'auth.json')})
assert w['plan_logins'] == want, w['plan_logins']
assert stat.S_IMODE(os.stat(path).st_mode) == 0o600, 'the config stays private'
PY

# --setup is the card path: it cannot carry --plan-logins (which turns the worker on).
if HOME="$home" PATH="$path" OPENVIBE_INSTALL_PLAN_LOGINS_DRY_RUN=1 \
	sh "$installer" --plan-logins --setup "osk_$(printf '%043d' 0)" >/dev/null 2>&1; then
	fail "--plan-logins was accepted with --setup"
fi

echo "install.sh --plan-logins: ok"
