#!/bin/sh
set -eu
pattern='OpenVibe[ .-]No'"'"'de|openvibe-'"'"'node|no'"'"'d_|node[.]openvibe[.]network|node:lnk_'
if git grep -n -I -i -E "$pattern" -- . ':!migrations/**' ':!scripts/test-link-brand.sh'; then
    echo 'old Link brand or principal ID returned' >&2
    exit 1
fi
