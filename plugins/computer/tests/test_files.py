"""The computer plugin's files: the folder is opened once, and the name is checked where it is used (F3).

The review of 2026-11-05 found that the plugin checked a path and then used the name again: a folder or a file
swapped for a link after the check moved the work outside the folders the owner allowed. These tests swap the
name (the file itself, the folder above it, the root) between the check and the use, and a hard-linked file, and
ask for the outside file to stay untouched.
"""
import base64
import io
import json
import os
import shutil
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
sys.path.insert(0, os.path.dirname(HERE))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(HERE)), "sdk"))

from openvibe_computer import Computer  # noqa: E402
from openvibe_plugin import Runtime  # noqa: E402
from test_computer import Out, reply  # noqa: E402

LINUX = sys.platform.startswith("linux")


@unittest.skipUnless(LINUX, "the handle-based file operations are Linux's")
class FileHandleTest(unittest.TestCase):
    """The files are reached through the folders' handles: a name that is swapped cannot move the work."""

    def boot(self, level="observe", **config):
        self.dir = tempfile.mkdtemp(prefix="ov-files-")
        self.outside = tempfile.mkdtemp(prefix="ov-outside-")
        self.addCleanup(shutil.rmtree, self.dir, ignore_errors=True)
        self.addCleanup(shutil.rmtree, self.outside, ignore_errors=True)
        os.mkdir(os.path.join(self.dir, "sub"))
        with open(os.path.join(self.dir, "notes.txt"), "w") as f:
            f.write("hello\n")
        with open(os.path.join(self.dir, "sub", "inner.txt"), "w") as f:
            f.write("inner\n")
        with open(os.path.join(self.outside, "secret.txt"), "w") as f:
            f.write("SECRET\n")
        with open(os.path.join(self.outside, "inner.txt"), "w") as f:
            f.write("SECRET\n")
        self.out = Out()
        self.plugin = Computer(env={}, platform="linux")
        self.rt = Runtime(self.plugin, infile=io.StringIO(""), outfile=self.out)
        self.rt.dispatch({"op": "hello", "config": {"level": level, "roots": [self.dir], **config}})
        self.n = 0
        return self.out.msgs()

    def cmd(self, kind, value, deadline_ms=30000):
        self.n += 1
        cid = "f%d" % self.n
        self.rt.dispatch({"op": "command", "id": cid, "kind": kind, "value": value, "deadline_ms": deadline_ms})
        return cid

    def swap_after_the_check(self, swap):
        """The swap happens right after the plugin checked the name, and before it uses it."""
        original = self.plugin._path

        def patched(raw, level, create=False):
            target = original(raw, level, create)
            swap()
            return target
        self.plugin._path = patched

    def wait_for_the_approval(self, cid):
        self.out.wait_for(lambda m: m.get("name") == "approval" and m.get("id") == cid)

    def link_out(self, path, target):
        """A link from a name in the folder to a file outside it."""
        os.symlink(target, path)

    # ---- the name is swapped after it was checked -----------------------------------------------
    def test_a_file_swapped_after_the_check_is_not_read(self):
        # F3: the file was there when the name was checked, and a link out of the folder replaced it.
        self.boot("observe")
        target = os.path.join(self.dir, "notes.txt")
        outside = os.path.join(self.outside, "secret.txt")

        def swap():
            os.remove(target)
            self.link_out(target, outside)
        self.swap_after_the_check(swap)
        r = reply(self.out, self.cmd("fs", {"op": "read", "path": target}))
        self.assertEqual(r["op"], "nack", "the file outside the folder was read: %r" % r)
        self.assertNotIn("SECRET", json.dumps(r))

    def test_a_folder_swapped_after_the_check_is_not_listed(self):
        # F3: the folder was there when the name was checked, and a link out of the root replaced it.
        self.boot("observe")
        target = os.path.join(self.dir, "sub")

        def swap():
            os.rename(target, target + "-old")
            self.link_out(target, self.outside)
        self.swap_after_the_check(swap)
        r = reply(self.out, self.cmd("fs", {"op": "list", "path": target}))
        self.assertEqual(r["op"], "nack", "the folder outside the roots was listed: %r" % r)
        self.assertNotIn("SECRET", json.dumps(r))

    def test_a_file_swapped_after_the_check_is_not_stated(self):
        # F3: the same for a stat: what is answered is the file outside the folder.
        self.boot("observe")
        target = os.path.join(self.dir, "notes.txt")
        outside = os.path.join(self.outside, "secret.txt")

        def swap():
            os.remove(target)
            self.link_out(target, outside)
        self.swap_after_the_check(swap)
        r = reply(self.out, self.cmd("fs", {"op": "stat", "path": target}))
        self.assertEqual(r["op"], "nack", "the file outside the folder was stated: %r" % r)

    def test_the_folder_above_the_name_swapped_after_the_check_is_not_read(self):
        # F3: every ancestor of the name, not only the name itself.
        self.boot("observe")
        folder = os.path.join(self.dir, "sub")
        target = os.path.join(folder, "inner.txt")

        def swap():
            os.rename(folder, folder + "-old")
            self.link_out(folder, self.outside)
        self.swap_after_the_check(swap)
        r = reply(self.out, self.cmd("fs", {"op": "read", "path": target}))
        self.assertEqual(r["op"], "nack", "the file outside the folder was read: %r" % r)
        self.assertNotIn("SECRET", json.dumps(r))

    # ---- the name is swapped while the owner is asked ------------------------------------------
    def test_a_write_whose_folder_is_swapped_while_it_waits_is_refused(self):
        # F3: the request waits for the owner; the folder above the file is replaced by a link out of the root.
        self.boot("ask")
        folder = os.path.join(self.dir, "sub")
        target = os.path.join(folder, "out.txt")
        cid = self.cmd("fs", {"op": "write", "path": target, "content_b64": "eA=="})
        self.wait_for_the_approval(cid)
        os.rename(folder, folder + "-old")
        self.link_out(folder, self.outside)
        self.plugin.on_approval(cid, True)
        r = reply(self.out, cid)
        self.assertEqual(r["op"], "nack", "the file outside the folder was written: %r" % r)
        self.assertFalse(os.path.exists(os.path.join(self.outside, "out.txt")), "a file was written outside")

    def test_an_overwrite_whose_folder_is_swapped_while_it_waits_is_refused(self):
        # F3: the same for an overwrite.
        self.boot("ask")
        folder = os.path.join(self.dir, "sub")
        target = os.path.join(folder, "inner.txt")
        cid = self.cmd("fs", {"op": "write", "path": target, "content_b64": "eA==", "mode": "overwrite"})
        self.wait_for_the_approval(cid)
        os.rename(folder, folder + "-old")
        self.link_out(folder, self.outside)
        self.plugin.on_approval(cid, True)
        r = reply(self.out, cid)
        self.assertEqual(r["op"], "nack", "the file outside the folder was written: %r" % r)
        with open(os.path.join(self.outside, "secret.txt")) as f:
            self.assertEqual(f.read(), "SECRET\n", "a file outside the folder was changed")

    def test_an_append_through_a_link_written_while_it_waits_is_refused(self):
        # F3: the file itself is replaced by a link while the owner is asked: the append must not follow it.
        self.boot("ask")
        target = os.path.join(self.dir, "notes.txt")
        outside = os.path.join(self.outside, "appended.txt")
        cid = self.cmd("fs", {"op": "write", "path": target, "content_b64": "eA==", "mode": "append"})
        self.wait_for_the_approval(cid)
        os.rename(target, target + "-old")
        self.link_out(target, outside)
        self.plugin.on_approval(cid, True)
        r = reply(self.out, cid)
        self.assertEqual(r["op"], "nack", "the file outside the folder was written: %r" % r)
        self.assertFalse(os.path.exists(outside), "the target of the link was created")

    def test_a_mkdir_whose_folder_is_swapped_while_it_waits_is_refused(self):
        # F3: the folder above the new one is replaced by a link while the owner is asked.
        self.boot("ask")
        folder = os.path.join(self.dir, "sub")
        target = os.path.join(folder, "made")
        cid = self.cmd("fs", {"op": "mkdir", "path": target})
        self.wait_for_the_approval(cid)
        os.rename(folder, folder + "-old")
        self.link_out(folder, self.outside)
        self.plugin.on_approval(cid, True)
        r = reply(self.out, cid)
        self.assertEqual(r["op"], "nack", "the folder outside the root was made: %r" % r)
        self.assertFalse(os.path.exists(os.path.join(self.outside, "made")), "a folder was made outside")

    def test_an_exec_cwd_swapped_while_it_waits_does_not_run_outside(self):
        # F3: the folder a program runs in is replaced by a link while the owner is asked.
        self.boot("ask")
        folder = os.path.join(self.dir, "sub")
        cid = self.cmd("exec", {"argv": [sys.executable, "-c", "open('marker.txt', 'w').write('x')"], "cwd": folder})
        self.wait_for_the_approval(cid)
        os.rename(folder, folder + "-old")
        self.link_out(folder, self.outside)
        self.plugin.on_approval(cid, True)
        r = reply(self.out, cid)
        self.assertEqual(r["op"], "nack", "the program ran in the folder outside the roots: %r" % r)
        self.assertFalse(os.path.exists(os.path.join(self.outside, "marker.txt")), "the program wrote outside")

    def test_an_exec_cwd_swapped_before_it_is_used_is_refused(self):
        # F3: the same, with the swap between the check and the start (no approval in the way).
        self.boot("trusted")
        folder = os.path.join(self.dir, "sub")

        def swap():
            os.rename(folder, folder + "-old")
            self.link_out(folder, self.outside)
        self.swap_after_the_check(swap)
        cid = self.cmd("exec", {"argv": [sys.executable, "-c", "open('marker.txt', 'w').write('x')"], "cwd": folder})
        r = reply(self.out, cid)
        self.assertEqual(r["op"], "nack", "the program ran in the folder outside the roots: %r" % r)
        self.assertFalse(os.path.exists(os.path.join(self.outside, "marker.txt")), "the program wrote outside")

    # ---- links -----------------------------------------------------------------------------------
    def test_a_dangling_link_does_not_create_its_target(self):
        # F3: a link to a file that is not there yet: writing through it must not create the file outside.
        self.boot("trusted")
        target = os.path.join(self.dir, "dangling")
        outside = os.path.join(self.outside, "made-by-the-link.txt")
        self.link_out(target, outside)
        r = reply(self.out, self.cmd("fs", {"op": "write", "path": target, "content_b64": "eA==", "mode": "append"}))
        self.assertEqual(r["op"], "nack", "the write through the link was accepted: %r" % r)
        self.assertFalse(os.path.exists(outside), "the target of the link was created")

    def test_a_link_out_of_the_root_is_refused(self):
        # F3: a link that was already there is refused as well.
        self.boot("observe")
        target = os.path.join(self.dir, "escape")
        self.link_out(target, self.outside)
        r = reply(self.out, self.cmd("fs", {"op": "list", "path": target}))
        self.assertEqual((r["op"], r["fault_code"]), ("nack", "not_allowed"), r)

    def test_the_root_swapped_keeps_the_work_in_the_folder_that_was_opened(self):
        # F3: the root itself is replaced by a link out of it: the plugin keeps the folder it opened.
        self.boot("ask")
        target = os.path.join(self.dir, "out.txt")
        cid = self.cmd("fs", {"op": "write", "path": target, "content_b64": "eA=="})
        self.wait_for_the_approval(cid)
        os.rename(self.dir, self.dir + "-moved")
        self.link_out(self.dir, self.outside)
        self.plugin.on_approval(cid, True)
        reply(self.out, cid)
        self.assertFalse(os.path.exists(os.path.join(self.outside, "out.txt")), "a file was written outside")

    def test_without_openat2_a_link_is_refused(self):
        # A kernel without openat2: the plugin walks the name itself and follows no link at all.
        self.boot("observe")
        self.plugin._handles.openat2 = False
        target = os.path.join(self.dir, "link")
        self.link_out(target, os.path.join(self.dir, "sub"))
        r = reply(self.out, self.cmd("fs", {"op": "list", "path": target}))
        self.assertEqual(r["op"], "nack", "the link was followed: %r" % r)


class HardLinkTest(unittest.TestCase):
    """A file with more than one name is another name for the same bytes: refused below full."""

    def boot(self, level="observe"):
        self.dir = tempfile.mkdtemp(prefix="ov-hard-")
        self.outside = tempfile.mkdtemp(prefix="ov-hard-out-")
        self.addCleanup(shutil.rmtree, self.dir, ignore_errors=True)
        self.addCleanup(shutil.rmtree, self.outside, ignore_errors=True)
        with open(os.path.join(self.dir, "notes.txt"), "w") as f:
            f.write("hello\n")
        self.out = Out()
        self.plugin = Computer(env={}, platform=sys.platform)
        self.rt = Runtime(self.plugin, infile=io.StringIO(""), outfile=self.out)
        self.rt.dispatch({"op": "hello", "config": {"level": level, "roots": [self.dir]}})
        self.n = 0

    def cmd(self, kind, value, deadline_ms=30000):
        self.n += 1
        cid = "h%d" % self.n
        self.rt.dispatch({"op": "command", "id": cid, "kind": kind, "value": value, "deadline_ms": deadline_ms})
        return cid

    def link(self):
        """The file in the folder, and a second name for it outside."""
        inside = os.path.join(self.dir, "notes.txt")
        outside = os.path.join(self.outside, "alias.txt")
        try:
            os.link(inside, outside)
        except (OSError, NotImplementedError, AttributeError) as e:
            self.skipTest("this machine cannot make a hard link: %s" % e)
        return inside, outside

    def test_a_hard_link_is_not_read(self):
        self.boot("observe")
        inside, outside = self.link()
        r = reply(self.out, self.cmd("fs", {"op": "read", "path": inside}))
        self.assertEqual((r["op"], r["fault_code"]), ("nack", "not_allowed"), r)

    def test_a_hard_link_is_not_appended_to(self):
        self.boot("trusted")
        inside, outside = self.link()
        r = reply(self.out, self.cmd("fs", {"op": "write", "path": inside, "content_b64": "eA==", "mode": "append"}))
        self.assertEqual((r["op"], r["fault_code"]), ("nack", "not_allowed"), r)
        with open(outside) as f:
            self.assertEqual(f.read(), "hello\n", "the file outside the folder was changed")

    def test_at_full_a_hard_link_is_read(self):
        # At full the folders do not limit anything, so the file is read as any other.
        self.boot("full")
        inside, outside = self.link()
        r = reply(self.out, self.cmd("fs", {"op": "read", "path": inside}))
        self.assertEqual(r["op"], "ack", r)
        self.assertEqual(base64.b64decode(r["data"]["content_b64"]), b"hello\n")
