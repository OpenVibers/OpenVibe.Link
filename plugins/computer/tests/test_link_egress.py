import os
import sys
import unittest
from types import SimpleNamespace
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.dirname(HERE))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(HERE)), "sdk"))
from openvibe_computer import Computer
from openvibe_computer.link_egress import LinkComputer


class LinkEgressTest(unittest.TestCase):
    def test_cloud_browser_gets_its_own_desk_before_start(self):
        plugin = LinkComputer()
        browser = SimpleNamespace(sandbox=["node", "desk-sandbox", "--egress", "public", "--"])
        desk = "desk-s0123456789abcdef"
        with mock.patch.object(Computer, "_desk_browser", return_value=browser), mock.patch.object(plugin, "_shared", return_value=True):
            self.assertIs(plugin._desk_browser(desk), browser)
            self.assertEqual(browser.sandbox[-3:], ["--link-egress-desk", desk, "--"])
            plugin._desk_browser(desk)
            self.assertEqual(browser.sandbox.count("--link-egress-desk"), 1)
        self.assertFalse(any("credential" in arg for arg in browser.sandbox))

    def test_personal_desks_keep_their_normal_start(self):
        plugin = LinkComputer()
        browser = SimpleNamespace(sandbox=None)
        with mock.patch.object(Computer, "_desk_browser", return_value=browser), mock.patch.object(plugin, "_shared", return_value=False):
            self.assertIs(plugin._desk_browser("desk-personal"), browser)
            self.assertIsNone(browser.sandbox)


if __name__ == "__main__":
    unittest.main()
