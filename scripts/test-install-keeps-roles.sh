#!/bin/sh
# shellcheck disable=SC2016,SC2015 # the greps look for literal shell text; fail() ends every || branch
# An update with no role flags keeps what the machine is (2026-11-17, the shared node: set up before Link v0.78 saved its
# flags, a plain update would have run its worker's and shared desks' service as openvibe-link, and left the computer
# and ai plugins at their old release). Checks, without touching this machine:
# - installed_service_user reads the installed service's account: root when it names none, nothing when none is
#   installed;
# - configured_plugins lists the plugins a config runs, and nothing for a missing or broken config;
# - the installer keeps that account on such an update, and installs every configured bundled plugin again.
set -eu
cd "$(dirname "$0")/.."
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

# The two helpers, out of the installer.
for f in installed_service_user configured_plugins; do
	awk -v f="$f" '$0 ~ "^"f"\\(\\) \\{" {p=1} p {print} p && /^}$/ {exit}' install/install.sh >> "$T/helpers.sh"
done
grep -q '^installed_service_user() {' "$T/helpers.sh" || fail "installed_service_user is not in install.sh"
grep -q '^configured_plugins() {' "$T/helpers.sh" || fail "configured_plugins is not in install.sh"

# A fake systemctl: $FAKE_LOAD and $FAKE_USER are what it reports.
mkdir -p "$T/bin"
cat > "$T/bin/systemctl" <<'EOF'
#!/bin/sh
case "$*" in
*LoadState*) printf '%s\n' "${FAKE_LOAD:-not-found}" ;;
*User*) printf '%s\n' "${FAKE_USER:-}" ;;
esac
EOF
chmod +x "$T/bin/systemctl"

run() { PATH="$T/bin:$PATH" SUDO="" sh -c ". '$T/helpers.sh'; $1"; }

[ "$(FAKE_LOAD=loaded FAKE_USER='' run installed_service_user)" = root ] || fail "a unit with no User= runs as root"
[ "$(FAKE_LOAD=loaded FAKE_USER=ada run installed_service_user)" = ada ] || fail "a unit's own account is kept"
[ "$(FAKE_LOAD=loaded FAKE_USER=openvibe-link run installed_service_user)" = openvibe-link ] || fail "openvibe-link is read too"
[ -z "$(FAKE_LOAD=not-found run installed_service_user)" ] || fail "no installed service: nothing"

printf '{"plugins":[{"name":"computer"},{"name":"ai"},{"name":"x; rm -rf /"},"odd"]}' > "$T/config.json"
[ "$(run "configured_plugins '$T/config.json'" | tr '\n' ' ')" = "computer ai " ] || fail "the configured plugins, names only: $(run "configured_plugins '$T/config.json'")"
[ -z "$(run "configured_plugins '$T/none.json'")" ] || fail "no config: nothing"
printf 'not json' > "$T/bad.json"
[ -z "$(run "configured_plugins '$T/bad.json'")" ] || fail "a broken config: nothing"

# The installer uses them: the account is kept before the openvibe-link default, and the configured plugins are installed.
grep -q 'KEEP_USER=$(installed_service_user)' install/install.sh || fail "the installer does not read the installed account"
k=$(grep -n 'if \[ -n "$KEEP_USER" \] && \[ "$KEEP_USER" != openvibe-link \]; then' install/install.sh | cut -d: -f1)
d=$(grep -n '	SVC_USER=openvibe-link$' install/install.sh | cut -d: -f1)
[ -n "$k" ] && [ -n "$d" ] && [ "$k" -lt "$d" ] || fail "the kept account must be decided before the openvibe-link default"
grep -q 'for p in $(configured_plugins "$CONF/config.json"); do' install/install.sh || fail "an update does not install the configured plugins"

printf 'ok: an update with no flags keeps the service account and the configured plugins current\n'
