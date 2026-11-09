"""Cloud desk startup integration, kept separate from the browser's blind-fill code.

Only a desk name crosses the process boundary. The Node reads its assigned session from Network;
credentials stay in the Node's proxy process and never enter Chrome's arguments or the model's tools.
"""
from openvibe_computer import Computer


class LinkComputer(Computer):
    def _desk_browser(self, desk, binary=None, env=None):
        browser = super()._desk_browser(desk, binary=binary, env=env)
        if self._shared() and desk.startswith("desk-s") and len(desk) == 22:
            if browser.sandbox and "--link-egress-desk" not in browser.sandbox:
                separator = browser.sandbox.index("--")
                browser.sandbox[separator:separator] = ["--link-egress-desk", desk]
        return browser
