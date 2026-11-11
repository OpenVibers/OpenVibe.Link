"""Per-task origin allowlists for the browser: navigation outside the task's origins is refused.

An origin is scheme + host + port (``https://example.com``, default ports filled in). The task names its list;
absent means legacy unrestricted. An explicit empty list allows nothing. Anything else-shaped is refused rather
than guessed: an allowlist that cannot be read is not an allowlist.
"""

import urllib.parse

DEFAULT_PORTS = {"http": 80, "https": 443}


class OriginError(ValueError):
    """An origins list, or a URL against it, that cannot be honored."""


def split_origin(value):
    """(scheme, host, port) for an http(s) origin, or None. No path, query, userinfo or fragment allowed."""
    if not isinstance(value, str):
        return None
    try:
        u = urllib.parse.urlsplit(value.strip())
    except ValueError:
        return None
    if u.scheme not in ("http", "https") or not u.hostname:
        return None
    if u.path not in ("", "/") or u.query or u.fragment or u.username or u.password:
        return None
    try:
        port = u.port or DEFAULT_PORTS[u.scheme]
    except ValueError:
        return None
    return (u.scheme, u.hostname.lower(), port)


def parse_origins(raw):
    """The task's allowlist, or None when the task names none (legacy: unrestricted). An explicit empty
    list allows nothing. Anything else-shaped raises OriginError: fail closed, never guessed."""
    if raw is None:
        return None
    if not isinstance(raw, list):
        raise OriginError("origins is a list of http(s) origins")
    if len(raw) > 32:
        raise OriginError("origins has at most 32 addresses")
    origins = []
    for item in raw:
        if not isinstance(item, str) or len(item) > 2048:
            raise OriginError("each origin is an http(s) address of at most 2048 characters")
        origin = split_origin(item)
        if origin is None:
            raise OriginError("origins holds http(s) origins like https://example.com, got %r" % (item,))
        origins.append(origin)
    return origins


def origin_of(url):
    """(scheme, host, port) for an http(s) URL of any path, or None when it has none (about:blank, data:, …)."""
    if not isinstance(url, str):
        return None
    try:
        u = urllib.parse.urlsplit(url.strip())
    except ValueError:
        return None
    if u.scheme not in ("http", "https") or not u.hostname:
        return None
    try:
        port = u.port or DEFAULT_PORTS[u.scheme]
    except ValueError:
        return None
    return (u.scheme, u.hostname.lower(), port)


def allowed(url, origins):
    """Whether the URL's origin is in the allowlist. None origins allow everything (legacy); otherwise the
    URL must parse to an allowed origin, and an unparseable URL is never allowed."""
    if origins is None:
        return True
    origin = origin_of(url)
    return origin is not None and origin in set(origins)
