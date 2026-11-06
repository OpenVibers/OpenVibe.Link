"""The computer plugin's files: the folders it may touch, and how it keeps that promise.

On Linux every allowed folder is opened **once**, as a handle, and every request is resolved
*relative to that handle*: with openat2 (RESOLVE_BENEATH | RESOLVE_NO_MAGICLINKS) where the
kernel has it (Linux 5.6 and later), and otherwise one component at a time with O_NOFOLLOW.
The object the plugin then reads, lists, writes or runs a program in is the one the handle points
at, so a name that is swapped after the check cannot move the work outside the folder.

macOS and Windows keep the name checks they always had: there the plugin compares path names,
and a name that changes under it is still a name (see docs/computer.md).

A file with more than one name (a hard link) is refused below level full: such a file is
another name for the same bytes, and the folder limit is about the object, not only about its
name.
"""

import ctypes
import errno
import os
import shutil
import stat
import sys
import tempfile

from openvibe_plugin import Fault

MAX_PATH_LEN = 4096

# openat2 (Linux 5.6 and later): the kernel resolves a name relative to a directory handle and
# refuses to leave it.
_SYS_OPENAT2 = 437
_AT_FDCWD = -100
RESOLVE_NO_MAGICLINKS = 0x02
RESOLVE_BENEATH = 0x08

_O_PATH = getattr(os, "O_PATH", 0)
_O_CLOEXEC = getattr(os, "O_CLOEXEC", 0)
_O_DIRECTORY = getattr(os, "O_DIRECTORY", 0)
_O_NOFOLLOW = getattr(os, "O_NOFOLLOW", 0)


class _How(ctypes.Structure):
    _fields_ = [("flags", ctypes.c_uint64), ("mode", ctypes.c_uint64), ("resolve", ctypes.c_uint64)]


def _libc():
    try:
        return ctypes.CDLL(None, use_errno=True)
    except OSError:
        return None


def _openat2(dirfd, path, flags, mode=0, resolve=0):
    """The kernel's own openat2(2): a name resolved under a handle, never outside it."""
    libc = _libc()
    how = _How(flags=flags, mode=mode, resolve=resolve)
    data = os.fsencode(path)
    buf = ctypes.create_string_buffer(data, len(data) + 1)
    ctypes.set_errno(0)
    rc = libc.syscall(ctypes.c_long(_SYS_OPENAT2), ctypes.c_int(dirfd), buf,
                      ctypes.byref(how), ctypes.c_size_t(ctypes.sizeof(how)))
    if rc < 0:
        err = ctypes.get_errno()
        raise OSError(err, os.strerror(err), path)
    return int(rc)


def _openat2_usable():
    """Whether this kernel has openat2."""
    libc = _libc()
    if libc is None or not hasattr(libc, "syscall"):
        return False
    try:
        fd = _openat2(_AT_FDCWD, ".", os.O_RDONLY | _O_DIRECTORY | _O_CLOEXEC, 0, RESOLVE_NO_MAGICLINKS)
    except Exception:
        return False
    os.close(fd)
    return True


class Handles:
    """The allowed folders, each opened once and kept open: the plugin never names one again."""

    def __init__(self):
        # The handles are Linux's; the machine's own platform decides, not what a test says about it.
        self.linux = sys.platform.startswith("linux")
        self.openat2 = self.linux and _openat2_usable()
        self._fds = {}

    def fd(self, path):
        """The handle of a folder: opened on the first use, then the same one every time."""
        fd = self._fds.get(path)
        if fd is None:
            try:
                fd = os.open(path, os.O_RDONLY | _O_DIRECTORY | _O_CLOEXEC)
            except FileNotFoundError:
                raise Fault("not_allowed", "%s does not exist" % path)
            except PermissionError:
                raise Fault("not_allowed", "the Node's account may not use %s" % path)
            except OSError as e:
                raise Fault("not_allowed", "%s cannot be used: %s" % (path, e.strerror))
            self._fds[path] = fd
        return fd

    def close(self):
        for fd in self._fds.values():
            try:
                os.close(fd)
            except OSError:
                pass
        self._fds = {}

    def open_beneath(self, base, rel, flags, mode=0, level="observe"):
        """Open a name under the handle of a folder. Returns the handle of what it names."""
        fd = self.fd(base)
        name = rel or "."
        if self.openat2:
            # Below full the name may not leave the folder; at full it may go anywhere the account can.
            resolve = 0 if level == "full" else (RESOLVE_BENEATH | RESOLVE_NO_MAGICLINKS)
            try:
                return _openat2(fd, name, flags, mode, resolve)
            except OSError as e:
                raise _fault(e, base, rel)
        return _walk(fd, name, flags, mode, level == "full")


def _fault(err, base, rel):
    """The fault a failed resolution is: what the kernel said, in the plugin's words."""
    where = os.path.join(base, rel) if rel else base
    if err.errno == errno.EXDEV:
        return Fault("not_allowed", "%s is outside the folder it was checked in" % where)
    if err.errno == errno.ELOOP:
        return Fault("not_allowed", "%s is a link this machine does not follow" % where)
    if err.errno == errno.ENOENT:
        return Fault("bad_value", "%s does not exist" % where)
    if err.errno in (errno.EACCES, errno.EPERM):
        return Fault("not_allowed", "the Node's account may not use %s" % where)
    if err.errno == errno.ENOTDIR:
        return Fault("bad_value", "%s is not a folder" % where)
    if err.errno == errno.EEXIST:
        return Fault("bad_value", "%s exists" % where)
    if err.errno == errno.EISDIR:
        return Fault("bad_value", "%s is a folder" % where)
    if err.errno == errno.ENAMETOOLONG:
        return Fault("bad_value", "%s has a name that is too long" % where)
    return Fault("not_allowed", "%s cannot be used: %s" % (where, err.strerror))


def _walk(fd, name, flags, mode=0, follow=False):
    """Without openat2: the name is walked one component at a time, under the handle of the folder.

    Below full no link is followed at all (a link is the one thing a name check cannot pin down);
    at full links are followed, as they always were.
    """
    parts = [p for p in name.split("/") if p and p != "."]
    if not parts:
        return os.dup(fd)
    # A ".." would climb out of the folder's handle. _path normalizes every name first, so none should reach here;
    # the walker refuses one anyway, so the folder limit does not rest on its caller (openat2's RESOLVE_BENEATH does
    # the same on the other path).
    if not follow and ".." in parts:
        raise Fault("not_allowed", "%s climbs out of the folder" % name)
    current = os.dup(fd)
    try:
        for i, part in enumerate(parts):
            last = i == len(parts) - 1
            if last:
                oflags = flags
            else:
                oflags = os.O_RDONLY | _O_DIRECTORY | _O_CLOEXEC
            if not follow:
                oflags |= _O_NOFOLLOW
            try:
                nxt = os.open(part, oflags, mode, dir_fd=current)
            except OSError as e:
                if e.errno in (errno.ELOOP, errno.EMLINK):
                    raise Fault("not_allowed", "%s is a link: this kernel has no openat2, so no link is followed" % part)
                raise
            os.close(current)
            current = nxt
    except BaseException:
        os.close(current)
        raise
    return current


def _resolved_path(fd):
    """The path a handle points at now (Linux): what the object is called."""
    try:
        return os.readlink("/proc/self/fd/%d" % fd)
    except OSError:
        return None


class Files:
    """The folders this machine allows, and the names an agent may ask for inside them."""

    def __init__(self, roots, forms, level, handles):
        self.roots = list(roots)
        self.forms = forms or {}
        self.level = level
        self.handles = handles
        # The handle-based operations are Linux's; macOS and Windows keep the name checks they always had.
        self.native = sys.platform.startswith("linux")

    def _inside(self, path):
        return any(path == r or path.startswith(r.rstrip(os.sep) + os.sep) for r in self.roots)

    def target(self, path, level, create="no", guard=None):
        """The target of one request: what it names, and the folder it is checked in.

        The name is resolved once here, so a name that leaves the folder is refused before the owner is asked;
        every later use resolves it again, under the same handle.
        """
        if not self.native:
            return self._by_name(path, level, create, guard)
        base, rel = self._split(path, level)
        return Target(self, base, rel, path, level, guard, create=create)

    def _split(self, path, level):
        """The folder a path belongs to, and the name under it."""
        if level == "full":
            return "/", path.lstrip("/")
        best = None
        for root in self.roots:
            for form in self.forms.get(root, [root]):
                form = form.rstrip(os.sep) or form
                if path == form:
                    rel = ""
                elif path.startswith(form + os.sep):
                    rel = path[len(form) + 1:]
                else:
                    continue
                if best is None or len(form) > len(best[0]):
                    best = (form, rel, root)
        if best is None:
            raise Fault("not_allowed", "%s is outside this machine's allowed folders" % path)
        return best[2], best[1]

    def _by_name(self, path, level, create, guard):
        """Without handles (macOS, Windows): the name itself is checked, as it always was."""
        if create != "no" and not os.path.lexists(path):
            real = os.path.join(os.path.realpath(os.path.dirname(path)), os.path.basename(path))
        else:
            real = os.path.realpath(path)
        if level != "full" and not self._inside(real):
            raise Fault("not_allowed", "%s is outside this machine's allowed folders" % real)
        return Target(self, None, None, path, level, guard, by_name=True, create=create, real=real)


class Target:
    """One path an agent asked for, and the handle it is reached through.

    Every operation resolves the name again, under the folder's handle: what the plugin then
    uses is the object the handle points at, not the name that was checked.
    """

    def __init__(self, files, base, rel, path, level, guard=None, by_name=False, create="no", real=None):
        self.files = files
        self.base = base
        self.rel = rel
        self.requested = path   # the name the agent asked for
        self.level = level
        self.guard = guard
        self.by_name = by_name
        self.create = create
        self.real = real         # without handles: the name resolved the way it always was
        self._display = None
        self.checked = None   # what the folder was when it was checked, by its own numbers

    @property
    def path(self):
        """Where the name points now: what the owner is shown, and what the answer names."""
        if self._display is None:
            self._display = self._compute_display()
        return self._display

    def _compute_display(self):
        """The path the object is at now, as the kernel sees it."""
        if self.by_name:
            if self.real is not None:
                return self.real
            return self._name_display()
        try:
            if self.create == "no":
                fd = self._open(_O_PATH | _O_CLOEXEC)
                try:
                    resolved = _resolved_path(fd)
                finally:
                    os.close(fd)
                if resolved:
                    return resolved
            else:
                parent, name = self._parent()
                try:
                    resolved = _resolved_path(parent)
                finally:
                    os.close(parent)
                if resolved:
                    return os.path.join(resolved, name)
        except Fault:
            pass
        return self._name_display()

    def _name_display(self):
        """The name resolved the way it was before the handles: what the path names."""
        if self.create == "no" or os.path.lexists(self.requested):
            return os.path.realpath(self.requested)
        return os.path.join(os.path.realpath(os.path.dirname(self.requested)), os.path.basename(self.requested))

    # ---- resolution ---------------------------------------------------------------------------------
    def _open(self, flags, mode=0):
        """The handle of the object this path names."""
        if self.by_name:
            return os.open(self.path, flags, mode)
        return self.files.handles.open_beneath(self.base, self.rel, flags, mode, self.level)

    def _parent(self):
        """The handle of the folder the name is in, and the name itself."""
        if self.by_name:
            parent = os.path.dirname(self.path) or os.sep
            return os.open(parent, os.O_RDONLY | _O_DIRECTORY | _O_CLOEXEC), os.path.basename(self.path)
        rel = self.rel
        cut = rel.rfind("/")
        if cut < 0:
            return os.dup(self.files.handles.fd(self.base)), rel
        return self.files.handles.open_beneath(self.base, rel[:cut], os.O_RDONLY | _O_DIRECTORY | _O_CLOEXEC,
                                               0, self.level), rel[cut + 1:]

    def _check(self, st, links=False):
        """The object is a regular file with more than one name: refused below full."""
        if links and self.level != "full" and stat.S_ISREG(st.st_mode) and st.st_nlink > 1:
            raise Fault("not_allowed",
                        "%s has %d names (a hard link): a file linked from outside the folder is refused"
                        % (self.path, st.st_nlink))
        if self.guard is not None:
            self.guard(st, self.path)

    def check_now(self, create):
        """Resolve the name once, so what cannot be used is refused before anything waits for an answer."""
        if self.by_name:
            return   # without handles the name is checked where it is used, as it always was
        if create == "no":
            self.stat()
        elif create == "parent":
            parent, name = self._parent()
            os.close(parent)

    def identity(self):
        """What the path names now, by its own numbers: a folder that was swapped is a different one."""
        fd = self.open_dir()
        try:
            st = os.fstat(fd)
        finally:
            os.close(fd)
        return (st.st_dev, st.st_ino)

    def remember(self):
        """Remember what this folder was when it was checked: a swap is seen later."""
        self.checked = self.identity()
        return self.checked

    # ---- operations --------------------------------------------------------------------------------
    def stat(self):
        """What the path names, as the kernel sees it now."""
        if self.by_name:
            try:
                return os.stat(self.path)
            except FileNotFoundError:
                raise Fault("bad_value", "%s does not exist" % self.path)
            except PermissionError:
                raise Fault("not_allowed", "the Node's account may not read %s" % self.path)
        fd = self._open(_O_PATH | _O_CLOEXEC)
        try:
            st = os.fstat(fd)
        finally:
            os.close(fd)
        self._check(st)
        return st

    def list(self, limit):
        """The names in a folder, as the kernel sees them now."""
        if self.by_name:
            try:
                with os.scandir(self.path) as it:
                    return self._entries(it, limit)
            except FileNotFoundError:
                raise Fault("bad_value", "%s does not exist" % self.path)
            except PermissionError:
                raise Fault("not_allowed", "the Node's account may not list %s" % self.path)
        fd = self._open(os.O_RDONLY | _O_DIRECTORY | _O_CLOEXEC)
        try:
            with os.scandir(fd) as it:
                return self._entries(it, limit)
        finally:
            os.close(fd)

    @staticmethod
    def _entries(it, limit):
        entries, total = [], 0
        for e in sorted(it, key=lambda e: e.name):
            total += 1
            if len(entries) >= limit:
                continue
            try:
                s = e.stat(follow_symlinks=False)
                entries.append({"name": e.name, "type": _kind(s.st_mode), "size": s.st_size, "mtime": int(s.st_mtime)})
            except OSError:
                entries.append({"name": e.name, "type": "other"})
        return entries, total

    def read(self, offset, length):
        """The bytes of the file, as the kernel sees them now."""
        if self.by_name:
            try:
                with open(self.path, "rb") as f:
                    st = os.fstat(f.fileno())
                    self._check(st, links=True)
                    f.seek(offset)
                    return f.read(length), st.st_size
            except FileNotFoundError:
                raise Fault("bad_value", "%s does not exist" % self.path)
            except PermissionError:
                raise Fault("not_allowed", "the Node's account may not read %s" % self.path)
        fd = self._open(os.O_RDONLY | _O_CLOEXEC)
        try:
            st = os.fstat(fd)
            self._check(st, links=True)
            data = os.pread(fd, length, offset)
            return data, st.st_size
        finally:
            os.close(fd)

    def write(self, data, mode):
        """Write into the file: a temporary file in its own folder, then the replace."""
        if self.by_name:
            self._write_by_name(data, mode)
            return
        if mode == "append":
            fd = self._open(os.O_WRONLY | os.O_APPEND | _O_CLOEXEC)
            try:
                self._check(os.fstat(fd), links=True)
                _write_all(fd, data)
            finally:
                os.close(fd)
            return
        parent, name = self._parent()
        try:
            if mode == "create":
                try:
                    os.stat(name, dir_fd=parent, follow_symlinks=False)
                except FileNotFoundError:
                    pass
                else:
                    raise Fault("bad_value", "%s exists (mode overwrite or append replaces or extends it)" % self.path)
            else:
                # What the name points at now: a file with two names is refused, and so is one of the Node's own.
                try:
                    st = os.stat(name, dir_fd=parent, follow_symlinks=True)
                except FileNotFoundError:
                    st = None
                if st is not None:
                    self._check(st, links=True)
            tmp = ".openvibe-%s" % os.urandom(8).hex()
            fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL | _O_CLOEXEC, 0o600, dir_fd=parent)
            try:
                _write_all(fd, data)
            finally:
                os.close(fd)
            try:
                st = os.stat(name, dir_fd=parent, follow_symlinks=False)
                os.chmod(tmp, stat.S_IMODE(st.st_mode), dir_fd=parent)
            except FileNotFoundError:
                pass
            os.replace(tmp, name, src_dir_fd=parent, dst_dir_fd=parent)
        except PermissionError:
            raise Fault("not_allowed", "the Node's account may not write %s" % self.path)
        except IsADirectoryError:
            raise Fault("bad_value", "%s is a folder" % self.path)
        except FileExistsError:
            raise Fault("bad_value", "%s exists (mode overwrite or append replaces or extends it)" % self.path)
        finally:
            os.close(parent)

    def _write_by_name(self, data, mode):
        """Without handles (macOS, Windows): the name is written as it always was."""
        path = self.path
        if os.path.isdir(path):
            raise Fault("bad_value", "%s is a folder" % path)
        if mode == "create" and os.path.lexists(path):
            raise Fault("bad_value", "%s exists (mode overwrite or append replaces or extends it)" % path)
        try:
            if mode == "append":
                with open(path, "ab") as f:
                    self._check(os.fstat(f.fileno()), links=True)
                    f.write(data)
            else:
                fd, tmp = tempfile.mkstemp(prefix=".openvibe-", dir=os.path.dirname(path))
                try:
                    with os.fdopen(fd, "wb") as f:
                        f.write(data)
                    if os.path.exists(path):
                        shutil.copymode(path, tmp)
                    os.replace(tmp, path)
                except BaseException:
                    if os.path.exists(tmp):
                        os.unlink(tmp)
                    raise
        except FileNotFoundError:
            raise Fault("bad_value", "%s does not exist" % path)
        except PermissionError:
            raise Fault("not_allowed", "the Node's account may not write %s" % path)

    def mkdir(self):
        """Make the folder, and the folders above it that are missing."""
        if self.by_name:
            existed = os.path.isdir(self.path)
            try:
                os.makedirs(self.path, exist_ok=True)
            except FileExistsError:
                raise Fault("bad_value", "%s exists and is not a folder" % self.path)
            except PermissionError:
                raise Fault("not_allowed", "the Node's account may not create %s" % self.path)
            return not existed
        parts = [p for p in (self.rel or "").split("/") if p]
        made = False
        for i, part in enumerate(parts):
            if i == 0:
                parent = os.dup(self.files.handles.fd(self.base))
            else:
                parent = self.files.handles.open_beneath(self.base, "/".join(parts[:i]),
                                                          os.O_RDONLY | _O_DIRECTORY | _O_CLOEXEC,
                                                          0, self.level)
            try:
                try:
                    os.mkdir(part, 0o777, dir_fd=parent)
                    if i == len(parts) - 1:
                        made = True
                except FileExistsError:
                    pass
                except PermissionError:
                    raise Fault("not_allowed", "the Node's account may not create %s" % self.path)
            finally:
                os.close(parent)
        return made

    def open_dir(self):
        """The handle of a folder, for a program to run in.

        A folder that was swapped since it was checked is refused: the program does not run in a name.
        """
        if self.by_name:
            try:
                return os.open(self.path, os.O_RDONLY | _O_DIRECTORY | _O_CLOEXEC)
            except FileNotFoundError:
                raise Fault("bad_value", "%s does not exist" % self.path)
            except NotADirectoryError:
                raise Fault("bad_value", "%s is not a folder" % self.path)
            except PermissionError:
                raise Fault("not_allowed", "the Node's account may not use %s" % self.path)
        fd = self._open(os.O_RDONLY | _O_DIRECTORY | _O_CLOEXEC)
        if self.checked is not None:
            st = os.fstat(fd)
            if (st.st_dev, st.st_ino) != self.checked:
                os.close(fd)
                raise Fault("not_allowed", "%s is not the folder that was checked" % self.path)
        return fd


def _write_all(fd, data):
    """Write every byte, or say so."""
    view = memoryview(data)
    while view:
        written = os.write(fd, view)
        view = view[written:]


def _kind(mode):
    """The kind of a file, as the plugin answers it."""
    if stat.S_ISDIR(mode):
        return "dir"
    if stat.S_ISLNK(mode):
        return "link"
    if stat.S_ISREG(mode):
        return "file"
    if stat.S_ISFIFO(mode):
        return "fifo"
    if stat.S_ISSOCK(mode):
        return "socket"
    return "other"
