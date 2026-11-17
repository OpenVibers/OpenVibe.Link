#!/bin/sh
# Link v0.78: a machine keeps itself current. Checks, without touching this machine:
# - the flags the installer saves for the updater never carry a pairing code, a setup key or a pinned version;
# - the updater (extracted from install.sh, as CI does the headless-shell one) installs a newer release only when nothing
#   runs here, only an install.sh that matches its release's SHA256SUMS, with the saved flags; and does nothing when
#   current or turned off.
set -eu
cd "$(dirname "$0")/.."
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT

# The saved flags.
s=$(grep -n '^SAVED_ARGS=""' install/install.sh | cut -d: -f1)
e=$(awk -v s="$s" 'NR>s && /^done$/{print NR; exit}' install/install.sh)
sed -n "${s},${e}p" install/install.sh > "$T/save.sh"
# shellcheck disable=SC2016 # the line is for save.sh, which expands it
printf 'printf "%%s" "$SAVED_ARGS"\n' >> "$T/save.sh"
saved=$(sh "$T/save.sh" --worker --code ABC123 --name "Ada's box" --setup sk_secret --version v0.1.0 --plan-logins --no-auto-update)
got=$(printf '%s' "$saved" | python3 -c 'import shlex, sys; print(shlex.split(sys.stdin.read()))')
[ "$got" = "['--worker', '--name', \"Ada's box\", '--plan-logins']" ] || { echo "saved flags: $got"; exit 1; }

# The updater, against a fake release site and a fake openvibe-link.
awk '/update-link && chmod 0755/{f=1;next} /^PY$/{if(f){exit}} f' install/install.sh > "$T/update-link.py"
python3 - "$T" <<'PY'
import hashlib, http.server, json, os, subprocess, sys, threading

T = sys.argv[1]
INSTALL = b"#!/bin/sh\necho \"$@\" > \"$MARK\"\n"
state = {"sums": hashlib.sha256(INSTALL).hexdigest()}


class Site(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def do_GET(self):
        if self.path == "/releases/latest":
            self.send_response(302)
            self.send_header("Location", "/releases/tag/v0.80.0")
            self.end_headers()
        elif self.path == "/releases/tag/v0.80.0":
            self.send_response(200)
            self.end_headers()
        elif self.path == "/releases/download/v0.80.0/install.sh":
            self.send_response(200)
            self.end_headers()
            self.wfile.write(INSTALL)
        elif self.path == "/releases/download/v0.80.0/SHA256SUMS":
            self.send_response(200)
            self.end_headers()
            self.wfile.write(("%s  openvibe-link-linux-amd64\n%s  install.sh\n" % ("0" * 64, state["sums"])).encode())
        else:
            self.send_response(404)
            self.end_headers()


srv = http.server.HTTPServer(("127.0.0.1", 0), Site)
threading.Thread(target=srv.serve_forever, daemon=True).start()
bin_dir = os.path.join(T, "bin")
os.makedirs(bin_dir)
status = os.path.join(T, "status.json")
with open(os.path.join(bin_dir, "openvibe-link"), "w") as f:
    f.write('#!/bin/sh\ncase "$1" in --version) echo "openvibe-link $HAVE";; status) cat "%s";; esac\n' % status)
os.chmod(os.path.join(bin_dir, "openvibe-link"), 0o755)
args = os.path.join(T, "install-args")
open(args, "w").write(" '--worker' '--name' 'Ada'\\''s box'\n")
mark = os.path.join(T, "ran")


def run(have, st, off=False):
    open(status, "w").write(json.dumps(st))
    if os.path.exists(mark):
        os.remove(mark)
    env = dict(os.environ, PATH=bin_dir + os.pathsep + os.environ["PATH"], HAVE=have, MARK=mark,
               OPENVIBE_LINK_REPO="http://127.0.0.1:%d" % srv.server_address[1], OPENVIBE_LINK_INSTALL_ARGS=args,
               OPENVIBE_LINK_NO_AUTO_UPDATE=os.path.join(T, "off" if off else "absent"))
    if off:
        open(os.path.join(T, "off"), "w").close()
    r = subprocess.run([sys.executable, "-I", os.path.join(T, "update-link.py")], env=env, capture_output=True, text=True, timeout=60)
    ran = open(mark).read().strip() if os.path.exists(mark) else None
    return r.returncode, r.stdout.strip(), ran


idle = {"jobs": [], "screen": {"state": "off"}}
code, out, ran = run("0.76.0", idle)
assert code == 0 and ran == "--version v0.80.0 --worker --name Ada's box", (code, out, ran)
code, out, ran = run("0.76.0", {"jobs": [{"id": "job_1"}], "screen": {"state": "off"}})
assert ran is None and "waiting: 1 job(s) running" in out, (out, ran)
code, out, ran = run("0.76.0", {"jobs": [], "screen": {"state": "live"}})
assert ran is None and "the screen is live" in out, (out, ran)
code, out, ran = run("0.80.0", idle)
assert ran is None and out.startswith("current"), (out, ran)
code, out, ran = run("0.76.0", idle, off=True)
assert ran is None and "off" in out, (out, ran)
os.remove(os.path.join(T, "off"))
state["sums"] = "f" * 64
code, out, ran = run("0.76.0", idle)
assert code == 1 and ran is None and "does not match" in out, (code, out, ran)
print("update-link: installs a newer release only when idle and checked, with the saved flags; current and off do nothing")
PY
echo "test-install-update: ok"
