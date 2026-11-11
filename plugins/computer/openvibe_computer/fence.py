"""Nonce-fenced untrusted text, and byte caps with the cut marked.

A page an agent reads can carry instructions for it (prompt injection). The fence tells the two apart: the
machine mints a fresh 128-bit nonce per result, after the text is fixed, and wraps the text in markers bearing
it. Injected text predates the nonce, so it cannot bear it except by guessing 128 bits; an agent treats only
text inside a fence whose two nonces match as the page's, and everything else as untrusted.

    <<<ov-text nonce:{hex} origin:{origin}>>>
    ...the page's text...
    <<<ov-text-end nonce:{hex}>>>

Long text is cut first and the cut is marked, so a fence never hides a truncation: the marker sits outside the
capped region. Binary answers (screenshots, file content) are never fenced and never capped here.
"""

import secrets
import re

NONCE_BYTES = 16
OPEN_FMT = "<<<ov-text nonce:%s origin:%s>>>"
CLOSE_FMT = "<<<ov-text-end nonce:%s>>>"
START = re.compile(r"^<<<ov-text nonce:([0-9a-f]{32}) origin:(?:https?://[A-Za-z0-9.:[\]-]+|unknown)>>>$")
END = re.compile(r"^<<<ov-text-end nonce:([0-9a-f]{32})>>>$")


def mint_nonce():
    """A fresh fence nonce: 128 random bits, hex."""
    return secrets.token_hex(NONCE_BYTES)


def fence(text, nonce=None, origin="unknown"):
    """Wrap text in a fresh (or given) nonce fence. Returns (fenced, nonce)."""
    nonce = nonce or mint_nonce()
    body = text if isinstance(text, str) else str(text or "")
    return "%s\n%s\n%s" % (OPEN_FMT % (nonce, origin), body, CLOSE_FMT % nonce), nonce


def parse_fenced(text):
    """The page text inside a valid fence, or None. Both markers must bear the same nonce."""
    if not isinstance(text, str):
        return None
    lines = text.split("\n")
    if len(lines) < 3:
        return None
    first, last = START.fullmatch(lines[0]), END.fullmatch(lines[-1])
    if first is None or last is None or first.group(1) != last.group(1):
        return None
    return "\n".join(lines[1:-1])


def cap_text(text, max_bytes):
    """Cut text at max_bytes of UTF-8 with the cut marked, never splitting a character. Short text comes
    back untouched and unfenced; the marker sits outside the capped region."""
    if not isinstance(text, str):
        text = str(text or "")
    raw = text.encode("utf-8")
    if len(raw) <= max_bytes:
        return text, False
    cut = raw[:max_bytes]
    while cut:
        try:
            body = cut.decode("utf-8")
            break
        except UnicodeDecodeError:
            cut = cut[:-1]
    else:
        body = ""
    return body + "\n…[truncated %d of %d bytes]" % (len(raw) - len(cut), len(raw)), True
