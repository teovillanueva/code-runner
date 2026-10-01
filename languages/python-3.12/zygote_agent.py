#!/usr/bin/env python3
"""code-runner Python zygote agent (Phase 11, AGENT-01..04 / ZHARD-01..06).

Runs INSIDE a privileged, long-lived pool container (one per language+version).
It pre-imports the manifest `preimport` set ONCE so children share those pages
copy-on-write (spike 005: 2.7x density; spike 006: per-child hardening is free),
then serves the worker over the framed relay protocol — one TCP connection per
job, one hardened child per job.

This is NOT exposed to the internet: the worker dials it on the pool container's
Docker-network IP. The relay carries no secrets, satisfying design RULE #1 (the
agent/parent is credential-free; it never holds a Redis/soketi FD). Defense in
depth: every child scrubs all fds>2 before running user code.

Hardening (design RULE #2, lifted from .planning/spikes/_harness/zygote_hardened.py
and isolation_probe.py — proven, do not reinvent):
  - distinct UID per job (UID_BASE+n, setgroups([]), setresgid/uid)
  - prctl(PR_SET_NO_NEW_PRIVS, 1)
  - private PID namespace via DOUBLE FORK (unshare CLONE_NEWPID once)
  - private network namespace (unshare CLONE_NEWNET) — child has no network
  - private mount namespace (unshare CLONE_NEWNS) + rec-private / +
    private /tmp tmpfs (size from limits) + remounted /proc
  - per-child cgroup leaf (v2: memory.max + pids.max; v1 hosts such as Fly:
    memory.limit_in_bytes + pids.max), placed by the parent using the session's
    real root-ns pid returned via the double-fork pipe. The layout is read from
    the mount table, never inferred from path existence: a tmpfs under
    /sys/fs/cgroup happily accepts makedirs + writes while enforcing nothing
  - dup2 the child-side socketpair fds onto 0/1/2, scrub all fds>2

ctypes gotcha (mandatory): argtypes/restype are declared on unshare/mount/prctl
or the large CLONE_* flags mis-marshal and the syscall returns EINVAL.

Pure stdlib + the baked sci stack — no external deps.

The image's default run path (`python main.py` via DockerSocketRunner) is
unaffected; this agent is only invoked when the worker launches the image as a
pool container with an explicit command:
    python /opt/zygote/zygote_agent.py
"""
import base64
import ctypes
import errno
import json
import os
import re
import runpy
import select
import signal
import socket
import struct
import sys
import threading
import time

# ---- Linux syscall constants -------------------------------------------------
CLONE_NEWNS = 0x00020000
CLONE_NEWPID = 0x20000000
CLONE_NEWNET = 0x40000000
MS_RDONLY = 1
MS_NOSUID = 2
MS_NODEV = 4
MS_NOEXEC = 8
MS_REC = 1 << 14
MS_PRIVATE = 1 << 18
PR_SET_NO_NEW_PRIVS = 38
PR_SET_PDEATHSIG = 1
PR_SET_CHILD_SUBREAPER = 36
SIGKILL = 9

libc = ctypes.CDLL(None, use_errno=True)
# Declaring argtypes/restype is MANDATORY: without it ctypes mis-marshals the
# large namespace flag constants (e.g. CLONE_NEWPID=0x20000000) and unshare()
# rejects them with EINVAL.
libc.unshare.argtypes = [ctypes.c_int]
libc.unshare.restype = ctypes.c_int
libc.mount.argtypes = [ctypes.c_char_p, ctypes.c_char_p, ctypes.c_char_p,
                       ctypes.c_ulong, ctypes.c_char_p]
libc.mount.restype = ctypes.c_int
libc.prctl.argtypes = [ctypes.c_int, ctypes.c_ulong, ctypes.c_ulong,
                       ctypes.c_ulong, ctypes.c_ulong]
libc.prctl.restype = ctypes.c_int


def _chk(rc, what):
    if rc != 0:
        e = ctypes.get_errno()
        raise OSError(e, f"{what} failed: {os.strerror(e)}")


def unshare(flags):
    _chk(libc.unshare(flags), f"unshare({flags:#x})")


def mount(src, tgt, fstype, flags, data):
    s = src.encode() if src else None
    t = tgt.encode()
    f = fstype.encode() if fstype else None
    d = data.encode() if data else None
    _chk(libc.mount(s, t, f, flags, d), f"mount({tgt})")


def prctl(option, arg2=0):
    _chk(libc.prctl(option, arg2, 0, 0, 0), f"prctl({option})")


def log(msg):
    sys.stderr.write(f"[zygote-agent] {msg}\n")
    sys.stderr.flush()


# ---- Config ------------------------------------------------------------------
RELAY_PORT = int(os.environ.get("ZYGOTE_RELAY_PORT", "7000"))
UID_BASE = int(os.environ.get("ZYGOTE_UID_BASE", "100000"))
DEFAULT_MEM_MAX = 128 * 1024 * 1024
DEFAULT_PIDS_MAX = 64
DEFAULT_TMPFS = 64 * 1024 * 1024
CGROOT = "/sys/fs/cgroup"

# ---- Relay frame types -------------------------------------------------------
# Worker -> agent
T_HELLO = 0x01
T_STDIN = 0x02
T_STDIN_CLOSE = 0x03
T_KILL = 0x04
# agent -> worker
T_STARTED = 0x10
T_STDOUT = 0x11
T_STDERR = 0x12
T_CPU = 0x13
T_EXIT = 0x14
T_ARTIFACT = 0x15

# Max single relay frame payload — MUST match maxFramePayload in zygote_relay.go
# (16 MiB). An artifact whose frame would exceed this is dropped + flagged
# (truncate + mark), mirroring the worker's per-cap artifact truncation.
MAX_FRAME_PAYLOAD = 16 * 1024 * 1024

CG_LAYOUT = None  # cgroup layout resolved at boot: {"mode": "v2"|"v1", ...} or None
_job_seq = 0    # monotonic per-job counter for unique UID + cgroup leaf names
_job_seq_lock = threading.Lock()
# os.fork() in a multithreaded process is only safe if no other thread holds a
# lock the child will need (notably CPython's import lock, which runpy re-acquires).
# Serialize the fork() so only one job double-forks at a time and no concurrent
# import can be mid-flight in another job's pre-run path. Relay is fully concurrent;
# only the brief spawn window is serialized.
_spawn_lock = threading.Lock()


def next_job_n():
    global _job_seq
    with _job_seq_lock:
        n = _job_seq
        _job_seq += 1
        return n


# ============================ Pre-import (RULE #4) ============================
def preimport_set():
    """Resolve the set to pre-import. argv[1] (comma list) > $ZYGOTE_PREIMPORT >
    the python manifest default."""
    raw = None
    if len(sys.argv) > 1 and sys.argv[1].strip():
        raw = sys.argv[1]
    elif os.environ.get("ZYGOTE_PREIMPORT"):
        raw = os.environ["ZYGOTE_PREIMPORT"]
    if raw:
        mods = [m.strip() for m in raw.split(",") if m.strip()]
    else:
        mods = ["numpy", "pandas", "scipy", "sklearn", "matplotlib.pyplot"]
    return mods


def do_preimport(mods):
    import importlib
    # matplotlib uses Agg via the image env (MPLBACKEND=Agg); be defensive anyway.
    try:
        import matplotlib  # noqa
        matplotlib.use("Agg")
    except Exception:
        pass
    ok = []
    for m in mods:
        try:
            importlib.import_module(m)
            ok.append(m)
        except Exception as e:  # a missing optional import must not kill the agent
            log(f"preimport {m} failed (non-fatal): {e}")
    log(f"pre-imported: {ok}")


# ============================ cgroup base (RULE #2) ===========================
# The layout is read from the MOUNT TABLE, never inferred from path existence.
# On Fly the pool container sees /sys/fs/cgroup as a TMPFS holding the
# cgroup-v1 controller mounts (memory/, pids/, cpu,cpuacct/, ...): the host is a
# v1 hybrid, the unified hierarchy has no controllers. os.makedirs() of a "leaf"
# on that tmpfs just creates ordinary directories, and writing memory.max /
# cgroup.procs / cgroup.kill there "succeeds" while enforcing nothing — which is
# how the agent ran for months believing it had per-child limits and a kill
# switch. A cgroupfs is only ever one of two mount types: "cgroup2" or "cgroup".

# cgroup-v1 controllers: the two we need for limits, plus CPU accounting if present.
V1_REQUIRED = ("memory", "pids")
V1_OPTIONAL = ("cpuacct",)


def _unescape_mountinfo(s):
    # mountinfo escapes space, tab, newline and backslash as \040 etc.
    return re.sub(r"\\([0-7]{3})", lambda m: chr(int(m.group(1), 8)), s)


def mount_entry(path, mountinfo="/proc/self/mountinfo"):
    """(fstype, root) of the mount at exactly `path`, e.g. ("cgroup", "/docker/<id>")
    or ("cgroup2", "/"); (None, None) when `path` is not a mount point. Fields
    per proc(5): id, parent, dev, ROOT, MOUNT POINT, opts, [optional...], "-",
    FSTYPE, source, super opts. The last matching line is the top-most mount."""
    want = os.path.normpath(path)
    found = (None, None)
    try:
        with open(mountinfo) as f:
            for line in f:
                fields = line.split()
                try:
                    sep = fields.index("-")
                except ValueError:
                    continue
                if len(fields) > sep + 1 and _unescape_mountinfo(fields[4]) == want:
                    found = (fields[sep + 1], _unescape_mountinfo(fields[3]))
    except OSError:
        return (None, None)
    return found


def fs_type(path, mountinfo="/proc/self/mountinfo"):
    return mount_entry(path, mountinfo)[0]


def _cgroup_dir(mount_point, cgroup_path):
    """Directory of the cgroup `cgroup_path` (as listed in /proc/self/cgroup)
    under the cgroupfs mounted at `mount_point`, honouring the mount's ROOT:
    Docker mounts the v1 controllers — and cgroup2 under a private cgroupns —
    with the container's OWN cgroup as the mount root, so `cgroup_path` maps to
    the mount point itself, not to <mount point>/<cgroup_path> (that would
    nest a second /docker/<id>/ under the first)."""
    _, root = mount_entry(mount_point)
    rel = cgroup_path
    if root and root != "/":
        if cgroup_path == root or cgroup_path.startswith(root + "/"):
            rel = cgroup_path[len(root):]
        else:
            rel = ""  # the mount exposes a subtree we're not below: it IS our view
    return os.path.join(mount_point, rel.lstrip("/"))


def read_proc_cgroup(path="/proc/self/cgroup"):
    """Parse /proc/self/cgroup into {hierarchy_id: (controllers, cgroup_path)}.
    The v2 (unified) entry has id 0 and an empty controller tuple; v1 entries
    list their controllers, e.g. 5:cpu,cpuacct:/docker/<id>."""
    out = {}
    with open(path) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            hid, ctrls, cpath = line.split(":", 2)
            out[int(hid)] = (tuple(c for c in ctrls.split(",") if c), cpath)
    return out


def setup_cgroup_base():
    """Resolve the cgroup layout used for per-child leaves. Returns
    {"mode": "v2", "base": dir} when /sys/fs/cgroup is a delegable cgroup2 tree,
    {"mode": "v1", "bases": {controller: dir}} on a v1/hybrid host, or None.
    Non-fatal: without a layout we still harden via UID + namespaces and kill
    sessions with SIGKILL; only the per-child memory/pids limits are skipped."""
    try:
        procs = read_proc_cgroup()
    except Exception as e:
        log(f"cgroup: cannot read /proc/self/cgroup ({e}); per-child limits skipped")
        return None

    if fs_type(CGROOT) == "cgroup2":
        layout = _setup_cgroup_v2(procs)
        if layout:
            return layout
    layout = _setup_cgroup_v1(procs)
    if layout:
        return layout
    kind = fs_type(CGROOT)
    if kind == "cgroup2":
        log("cgroup: cgroup2 is mounted but could not be delegated (see above); "
            "per-child memory/pids limits skipped")
    else:
        log(f"cgroup: {CGROOT} is neither a cgroup2 tree nor a cgroup-v1 hierarchy "
            f"(mount type={kind!r}); per-child memory/pids limits skipped")
    return None


def _setup_cgroup_v2(procs):
    """Delegated cgroup-v2 subtree (pool runs --privileged --cgroupns=host)."""
    try:
        # Docker Desktop mounts cgroup2 read-only by default; a privileged agent
        # (CAP_SYS_ADMIN) can remount it rw. Best-effort — on Linux/Fly with a
        # delegated cgroup it is already rw and this is a no-op.
        try:
            mount("none", CGROOT, None, MS_REC | (1 << 5), None)  # MS_REMOUNT
        except OSError:
            pass
        own = _cgroup_dir(CGROOT, procs[0][1])  # "0::/path"
        base = os.path.join(own, "zygote")
        mgr = os.path.join(base, "mgr")
        os.makedirs(mgr, exist_ok=True)
        # move ourselves into the mgr leaf so `base` has no member procs
        with open(os.path.join(mgr, "cgroup.procs"), "w") as f:
            f.write(str(os.getpid()))
        with open(os.path.join(base, "cgroup.subtree_control"), "w") as f:
            f.write("+memory +pids")
        log(f"cgroup v2: delegated subtree at {base}")
        return {"mode": "v2", "base": base}
    except Exception as e:
        log(f"cgroup v2 delegation unavailable ({e})")
        return None


def _setup_cgroup_v1(procs):
    """cgroup-v1 (hybrid) host: one mount per controller group under CGROOT, e.g.
    /sys/fs/cgroup/memory/docker/<id>. v1 lets a cgroup hold both processes and
    child cgroups, so leaves go straight under <own>/zygote (no mgr move)."""
    bases = {}
    for ctrls, cpath in procs.values():
        for ctrl in ctrls:
            if ctrl not in V1_REQUIRED + V1_OPTIONAL:
                continue
            # the mount is usually named after the full controller list
            # ("cpu,cpuacct"); some distros add per-controller symlinks.
            for cand in (",".join(ctrls),) + tuple(ctrls):
                mnt = os.path.join(CGROOT, cand)
                if fs_type(mnt) == "cgroup":
                    bases[ctrl] = os.path.join(_cgroup_dir(mnt, cpath), "zygote")
                    break
    if any(c not in bases for c in V1_REQUIRED):
        return None
    try:
        for b in bases.values():
            os.makedirs(b, exist_ok=True)
    except Exception as e:
        log(f"cgroup v1 base unavailable ({e})")
        return None
    log(f"cgroup v1: bases {bases}")
    return {"mode": "v1", "bases": bases}


def _write(path, value):
    with open(path, "w") as f:
        f.write(value)


def make_cgroup_leaf(child_pid, n, mem_max, pids_max):
    """Create the per-job cgroup leaf(s), set limits, place the session pid.
    Returns {"mode", "paths": {role: dir}} on FULL success, else None — never a
    partially-enforced leaf (the Go side trusts cgroupEnforced)."""
    if CG_LAYOUT is None:
        return None
    created = []
    try:
        if CG_LAYOUT["mode"] == "v2":
            leaf = os.path.join(CG_LAYOUT["base"], f"job{n}")
            os.makedirs(leaf, exist_ok=True)
            created.append(leaf)
            _write(os.path.join(leaf, "memory.max"), str(mem_max))
            _write(os.path.join(leaf, "pids.max"), str(pids_max))
            _write(os.path.join(leaf, "cgroup.procs"), str(child_pid))
            return {"mode": "v2", "paths": {"unified": leaf}}
        paths = {}
        for ctrl, base in CG_LAYOUT["bases"].items():
            d = os.path.join(base, f"job{n}")
            os.makedirs(d, exist_ok=True)
            created.append(d)
            paths[ctrl] = d
        _write(os.path.join(paths["memory"], "memory.limit_in_bytes"), str(mem_max))
        # memory+swap cap so the child cannot spill into swap. The file is absent
        # (ENOENT) when swap accounting is off; then memory.limit_in_bytes alone
        # is the cap, which is what we want anyway.
        try:
            _write(os.path.join(paths["memory"], "memory.memsw.limit_in_bytes"), str(mem_max))
        except OSError as e:
            if e.errno not in (errno.ENOENT, errno.EINVAL, errno.EPERM, errno.EACCES):
                raise
        _write(os.path.join(paths["pids"], "pids.max"), str(pids_max))
        for d in paths.values():
            _write(os.path.join(d, "cgroup.procs"), str(child_pid))
        return {"mode": "v1", "paths": paths}
    except Exception as e:
        log(f"cgroup leaf job{n} failed: {e}")
        for d in created:
            try:
                os.rmdir(d)
            except OSError:
                pass
        return None


def cgroup_cpu_ms(leaf):
    """Cumulative CPU (ms) of the leaf: v2 cpu.stat usage_usec, v1 cpuacct.usage
    (ns). None when unavailable (caller falls back to /proc/<pid>/stat)."""
    if not leaf:
        return None
    try:
        if leaf["mode"] == "v2":
            with open(os.path.join(leaf["paths"]["unified"], "cpu.stat")) as f:
                for line in f:
                    if line.startswith("usage_usec"):
                        return int(line.split()[1]) // 1000
            return None
        d = leaf["paths"].get("cpuacct")
        if not d:
            return None
        with open(os.path.join(d, "cpuacct.usage")) as f:
            return int(f.read().strip()) // 1_000_000
    except Exception:
        return None


_CLK_TCK = os.sysconf("SC_CLK_TCK") if hasattr(os, "sysconf") else 100


def proc_cpu_ms(realpid):
    """Fallback CPU reader from /proc/<pid>/stat (utime+stime in clock ticks).
    Used when no per-child cgroup leaf is available (e.g. Docker Desktop without
    delegated cgroups). On Fly/Linux the cgroup cpu.stat path is preferred."""
    if not realpid:
        return None
    try:
        with open(f"/proc/{realpid}/stat") as f:
            data = f.read()
        # fields after the (comm) which may contain spaces/parens
        rparen = data.rfind(")")
        fields = data[rparen + 2:].split()
        utime = int(fields[11])  # field 14 overall
        stime = int(fields[12])  # field 15 overall
        return (utime + stime) * 1000 // _CLK_TCK
    except Exception:
        return None


def cpu_ms(leaf, realpid):
    ms = cgroup_cpu_ms(leaf)
    if ms is not None:
        return ms
    return proc_cpu_ms(realpid)


def kill_session(leaf, realpid):
    """Kill the whole session tree. The session is PID 1 of its own pid namespace,
    so SIGKILL on it takes every descendant down with it whether or not we have
    a cgroup (the kernel kills a pid namespace when its init dies). cgroup.kill
    (v2) / walking the leaf's cgroup.procs (v1) are additional, best-effort."""
    if realpid:
        try:
            os.kill(realpid, SIGKILL)
        except OSError:
            pass
    if not leaf:
        return
    try:
        if leaf["mode"] == "v2":
            _write(os.path.join(leaf["paths"]["unified"], "cgroup.kill"), "1")
        else:
            with open(os.path.join(leaf["paths"]["memory"], "cgroup.procs")) as f:
                for pid in f.read().split():
                    try:
                        os.kill(int(pid), SIGKILL)
                    except (OSError, ValueError):
                        pass
    except Exception as e:
        log(f"cgroup kill {leaf['paths']} failed: {e}")


def cgroup_remove(leaf):
    if not leaf:
        return
    pending = list(leaf["paths"].values())
    # the kernel only lets us rmdir an empty leaf; wait briefly for procs to drain.
    for _ in range(50):
        for d in list(pending):
            try:
                os.rmdir(d)
                pending.remove(d)
            except OSError as e:
                if e.errno == errno.ENOENT:
                    pending.remove(d)
        if not pending:
            return
        time.sleep(0.02)
    log(f"cgroup leaf {pending} not removable (procs may linger)")


def _cgroup_procs_path(leaf):
    paths = leaf["paths"]
    return os.path.join(paths.get("unified") or paths["memory"], "cgroup.procs")


# ============================ Child body (RULE #2) ===========================
def _harden_child(n, mem_bytes, tmpfs_bytes):
    """Per-child hardening applied AFTER fork, BEFORE user code. Caller has ALREADY
    put us in a fresh PID namespace (the intermediate unshared CLONE_NEWPID before
    forking us). Order: namespaces & mounts first (need privilege), then drop
    privileges (no_new_privs + UID) last."""
    # private network namespace — child has no network even though the pool does
    try:
        unshare(CLONE_NEWNET)
    except OSError as e:
        log(f"child {n}: CLONE_NEWNET: {e}")

    # private mount namespace + private /tmp + /proc reflecting our pidns
    unshare(CLONE_NEWNS)
    try:
        mount("none", "/", None, MS_REC | MS_PRIVATE, None)
    except OSError:
        pass
    tmp_kb = max(tmpfs_bytes // 1024, 1024)
    try:
        # mode=1777 so the post-drop child uid can create its workdir + files
        mount("tmpfs", "/tmp", "tmpfs", MS_NOSUID | MS_NODEV,
              f"size={tmp_kb}k,mode=1777")
    except OSError as e:
        log(f"child {n}: tmpfs /tmp: {e}")
    try:
        mount("proc", "/proc", "proc", MS_NOSUID | MS_NODEV | MS_NOEXEC, None)
    except OSError as e:
        log(f"child {n}: proc remount: {e}")

    # drop privileges LAST
    prctl(PR_SET_NO_NEW_PRIVS, 1)
    uid = UID_BASE + n
    try:
        os.setgroups([])
    except OSError:
        pass
    os.setresgid(uid, uid, uid)
    os.setresuid(uid, uid, uid)


def _scrub_fds(keep):
    """Close every fd > 2 except those in `keep` (RULE #1 defense-in-depth).
    Done in the child before user code so no inherited socket survives."""
    keep = set(keep)
    try:
        fds = [int(d) for d in os.listdir("/proc/self/fd") if d.isdigit()]
    except OSError:
        fds = list(range(3, 4096))
    for fd in fds:
        if fd > 2 and fd not in keep:
            try:
                os.close(fd)
            except OSError:
                pass


CHILD_WORKDIR = "/tmp/work"  # inside the child's OWN private /tmp tmpfs


def _materialize_files(files, entrypoint):
    """Write the job's files into the child's private /tmp/work (which lives in the
    child's own tmpfs — invisible to sibling children). Returns (cwd, input_rel)
    where input_rel is the set of forward-slash relative paths of the materialized
    inputs (used to exclude them from artifact capture, matching the worker's
    exclude set). The files arrive copy-on-write through fork(); nothing is shared
    on a common filesystem, so cross-child file isolation is total."""
    os.makedirs(CHILD_WORKDIR, exist_ok=True)
    root = os.path.normpath(CHILD_WORKDIR)
    input_rel = set()
    for f in files or []:
        name = f.get("name")
        if not name:
            continue
        dest = os.path.normpath(os.path.join(CHILD_WORKDIR, name))
        if not (dest == root or dest.startswith(root + os.sep)):
            continue
        os.makedirs(os.path.dirname(dest), exist_ok=True)
        # Record the input's path relative to the workspace root, forward-slashed
        # to match the worker's SanitizeWorkspacePath exclude keys (e.g. "main.py"
        # or "data/in.csv"), so an input file is never returned as an artifact.
        input_rel.add(os.path.relpath(dest, root).replace(os.sep, "/"))
        content = f.get("content", "")
        encoding = f.get("encoding") or "utf8"
        if encoding == "base64":
            # Arbitrary bytes travelled as a JSON base64 string; decode and
            # write binary. base64.b64decode raises on malformed input, which
            # surfaces as a job error rather than silently writing garbage.
            raw = base64.b64decode(content) if isinstance(content, str) else content
            with open(dest, "wb") as fh:
                fh.write(raw)
        elif isinstance(content, str):
            with open(dest, "w") as fh:
                fh.write(content)
        else:
            with open(dest, "wb") as fh:
                fh.write(content)
    return CHILD_WORKDIR, input_rel


# ============================ Artifact capture (R4/R5) ========================
def _writeall(fd, buf):
    """Write all of buf to a raw fd (os.write may do a partial write on a full
    socket buffer; loop until drained). The agent drains the read end concurrently
    in relay_loop, so a large artifact never deadlocks."""
    view = memoryview(buf)
    while view:
        n = os.write(fd, view)
        view = view[n:]


def _flush_matplotlib_figures():
    """Save every still-open matplotlib figure to the CWD as figure_NNN.png, so a
    job that renders plots WITHOUT calling savefig() still produces artifacts.

    This is the zygote-tier counterpart of sitecustomize.py's atexit saver. On the
    Docker tier the sandbox runs `python main.py` and the interpreter shuts down
    normally, so sitecustomize's atexit hook fires and writes the figures. The
    zygote child runs user code via runpy.run_path() and exits with os._exit() —
    runpy does NOT trigger interpreter shutdown and os._exit() bypasses atexit
    entirely, so that hook NEVER runs here. We therefore drive the same flush
    explicitly from the child's finally block, while the CWD is still the job
    workspace, so _capture_artifacts() picks the PNGs up like any other file.

    Path is RELATIVE (figure_NNN.png) so it lands in the child's workspace
    (/tmp/work) — NOT the hardcoded /workspace, which does not exist in the
    child's private mount namespace.

    Best-effort throughout: a job that never imported matplotlib emits ZERO
    figures (sys.modules.get avoids forcing an import → no blank PNGs), and a
    single bad figure never alters the job's exit status."""
    plt = sys.modules.get("matplotlib.pyplot")
    if plt is None:
        return
    try:
        for num in plt.get_fignums():
            try:
                plt.figure(num).savefig(
                    "figure_%03d.png" % num, dpi=100, bbox_inches="tight"
                )
            except Exception:
                # A single bad figure must never crash the capture path.
                pass
    except Exception:
        pass


def _capture_artifacts(workdir, input_rel, art_fd):
    """Runs in the hardened child (post-uid-drop) AFTER user code, regardless of
    exit path — mirroring the Docker tier, which reads /workspace once the process
    terminates. Walks the child's private workspace and streams every NEW regular
    file back to the agent over the dedicated artifact fd as a record:

        [4-byte BE nameLen][name UTF-8][4-byte BE dataLen][data]

    Input files (and the entrypoint) are excluded by relative path; the worker
    re-applies the authoritative exclude set. Symlinks and non-regular files are
    skipped (parity with the Docker tar TypeReg filter). Best-effort throughout:
    any error is swallowed so artifact capture never alters the job's exit status."""
    try:
        for dirpath, _dirnames, filenames in os.walk(workdir):
            for fn in filenames:
                full = os.path.join(dirpath, fn)
                try:
                    if os.path.islink(full) or not os.path.isfile(full):
                        continue
                    rel = os.path.relpath(full, workdir).replace(os.sep, "/")
                    if rel in input_rel:
                        continue
                    with open(full, "rb") as fh:
                        data = fh.read()
                    name_b = rel.encode()
                    hdr = struct.pack(">I", len(name_b)) + name_b + struct.pack(">I", len(data))
                    _writeall(art_fd, hdr)
                    _writeall(art_fd, data)
                except OSError:
                    continue
    except Exception:
        pass


def _run_user_code(files, entrypoint, child_in, child_out, child_err, child_art,
                   n, mem_bytes, tmpfs_bytes):
    """Runs in the session (PID 1 of a fresh pidns). Wire stdio, harden, scrub,
    materialize files into the private tmpfs, chdir, exec the entrypoint in-process
    via runpy. Then — in ALL exit paths — capture workspace artifacts and stream
    them back over child_art before exiting with the script status.

    child_art is the child end of the dedicated artifact socketpair; it is kept
    open across the fd scrub so post-run capture can stream files to the agent
    (the workspace lives in this child's private mount namespace, unreachable from
    the agent). User code runs with this fd open — an acceptable, bounded exposure:
    the worst a job could do is fabricate "artifacts" it could equally have written
    as real files; the relay carries no secrets (design RULE #1)."""
    exit_code = 0
    workdir = CHILD_WORKDIR
    input_rel = set()
    try:
        # dup the child-side socketpair ends onto 0/1/2 BEFORE scrubbing.
        os.dup2(child_in, 0)
        os.dup2(child_out, 1)
        os.dup2(child_err, 2)
        # harden (namespaces/mounts/uid) while we still have privilege & before scrub
        _harden_child(n, mem_bytes, tmpfs_bytes)
        # write files into the child's OWN private /tmp tmpfs (post-mount, post-uid:
        # owned by the child uid, unreachable from siblings)
        workdir, input_rel = _materialize_files(files, entrypoint)
        # scrub every inherited fd>2 (relay socket, redis/soketi if any, pipes),
        # KEEPING the artifact fd so post-run capture can stream files back.
        _scrub_fds(keep=(child_art,))
        os.chdir(workdir)
        # reset Python signal handlers to default for user code
        signal.signal(signal.SIGTERM, signal.SIG_DFL)
        runpy.run_path(os.path.join(workdir, entrypoint), run_name="__main__")
    except SystemExit as se:
        code = se.code
        if code is None:
            exit_code = 0
        elif isinstance(code, int):
            exit_code = code & 0xFF
        else:
            sys.stderr.write(f"{code}\n")
            exit_code = 1
    except BaseException as e:  # surface user errors on the child's stderr (fd 2)
        try:
            import traceback
            traceback.print_exc()
        except Exception:
            sys.stderr.write(f"{type(e).__name__}: {e}\n")
        exit_code = 1
    finally:
        # Flush still-open matplotlib figures to the workspace BEFORE capture:
        # the zygote child never runs atexit (runpy + os._exit), so the no-savefig
        # auto-capture sitecustomize relies on would otherwise be lost (#369).
        try:
            _flush_matplotlib_figures()
        except Exception:
            pass
        # Capture + stream artifacts regardless of exit path, then close the
        # artifact fd so the agent sees EOF and stops reading for this job.
        try:
            _capture_artifacts(workdir, input_rel, child_art)
        except Exception:
            pass
        try:
            os.close(child_art)
        except OSError:
            pass
    os._exit(exit_code)


def spawn_session(files, entrypoint, n, mem_bytes, tmpfs_bytes,
                  child_in, child_out, child_err, child_art):
    """DOUBLE FORK for a per-child PID namespace. A process may unshare
    CLONE_NEWPID only once, so a thin intermediate unshares then forks the real
    session as PID 1 of the new pidns and exits. The session reparents to this
    agent (PR_SET_CHILD_SUBREAPER). Returns the session's real (root-ns) pid."""
    r, w = os.pipe()
    inter = os.fork()
    if inter == 0:
        # ---- intermediate ----
        try:
            os.close(r)
            unshare(CLONE_NEWPID)  # first & only call in this fresh process -> OK
            g = os.fork()
            if g == 0:
                # ---- session = PID 1 of the new pidns ----
                os.close(w)
                _run_user_code(files, entrypoint, child_in, child_out, child_err,
                               child_art, n, mem_bytes, tmpfs_bytes)  # never returns
            os.write(w, f"{g}\n".encode())
            os.close(w)
        except BaseException as e:
            try:
                os.write(w, f"ERR {e}\n".encode())
            except Exception:
                pass
        os._exit(0)  # intermediate exits; session reparents to the agent
    # ---- agent (parent) ----
    os.close(w)
    data = b""
    while not data.endswith(b"\n"):
        chunk = os.read(r, 64)
        if not chunk:
            break
        data += chunk
    os.close(r)
    # reap the intermediate (the subreaper inherits the session, not this)
    try:
        os.waitpid(inter, 0)
    except OSError:
        pass
    s = data.strip()
    if not s or s.startswith(b"ERR"):
        raise RuntimeError(f"session spawn failed: {s!r}")
    return int(s)


# ============================ Relay framing ==================================
def send_frame(conn, ftype, payload=b""):
    if isinstance(payload, str):
        payload = payload.encode()
    hdr = struct.pack(">BI", ftype, len(payload))
    conn.sendall(hdr + payload)


def send_json(conn, ftype, obj):
    send_frame(conn, ftype, json.dumps(obj).encode())


class ArtifactReader:
    """Incremental parser for the child->agent artifact stream. Each record is
    [4 BE nameLen][name][4 BE dataLen][data]; iter_records() yields complete
    (name, data) tuples as they arrive over the non-blocking artifact socket."""

    def __init__(self):
        self.buf = bytearray()

    def feed(self, data):
        self.buf.extend(data)

    def iter_records(self):
        while True:
            if len(self.buf) < 4:
                return
            (name_len,) = struct.unpack(">I", self.buf[:4])
            head = 4 + name_len + 4
            if len(self.buf) < head:
                return
            name = bytes(self.buf[4:4 + name_len]).decode("utf-8", "replace")
            (data_len,) = struct.unpack(">I", self.buf[4 + name_len:head])
            total = head + data_len
            if len(self.buf) < total:
                return
            data = bytes(self.buf[head:total])
            del self.buf[:total]
            yield name, data


def _send_artifacts(conn, artifacts):
    """Re-frame accumulated (name, data) records as T_ARTIFACT frames on the worker
    conn (payload: [4 BE nameLen][name][data]; data length implied by the frame).
    An artifact whose frame would exceed MAX_FRAME_PAYLOAD is dropped and flips the
    truncation flag (same truncate+mark outcome as the worker's per-cap skip). The
    conn is set blocking so a large frame is delivered in full. ALL frames are sent
    BEFORE the EXIT frame, so the worker has the complete set when Wait resolves.
    Returns True if any artifact was dropped."""
    truncated = False
    conn.setblocking(True)
    for name, data in artifacts:
        name_b = name.encode()
        if 4 + len(name_b) + len(data) > MAX_FRAME_PAYLOAD:
            truncated = True
            continue
        payload = struct.pack(">I", len(name_b)) + name_b + data
        try:
            send_frame(conn, T_ARTIFACT, payload)
        except OSError:
            truncated = True
            break
    return truncated


class FrameReader:
    """Incremental frame parser over a non-blocking socket. feed() drains the
    socket; iter_frames() yields complete (type, payload) tuples."""

    def __init__(self):
        self.buf = bytearray()

    def feed(self, data):
        self.buf.extend(data)

    def iter_frames(self):
        while True:
            if len(self.buf) < 5:
                return
            ftype, length = struct.unpack(">BI", self.buf[:5])
            if len(self.buf) < 5 + length:
                return
            payload = bytes(self.buf[5:5 + length])
            del self.buf[:5 + length]
            yield ftype, payload


# ============================ Per-job handling ==============================
def handle_job(conn):
    """Serve one job over `conn`. One connection == one job == one hardened child.
    Never raises out: any failure ends the job with an EXIT(error) frame."""
    conn.setblocking(True)
    reader = FrameReader()
    n = next_job_n()
    leaf = None
    realpid = None
    # parent-side socketpair ends (agent keeps these; child gets the other end)
    p_in = c_in = p_out = c_out = p_err = c_err = p_art = c_art = None
    started = False
    exited = False

    def cleanup():
        for sock in (p_in, c_in, p_out, c_out, p_err, c_err, p_art, c_art):
            if sock is not None:
                try:
                    sock.close()
                except OSError:
                    pass
        # Always SIGKILL the session on the way out (works with or without a
        # cgroup), then drop the leaf. Idempotent: the relay loop usually did it.
        kill_session(leaf, realpid)
        cgroup_remove(leaf)

    try:
        # ---- read HELLO (blocking until the first complete frame) ----
        hello = None
        while hello is None:
            data = conn.recv(65536)
            if not data:
                return  # conn closed before HELLO; nothing to do
            reader.feed(data)
            for ftype, payload in reader.iter_frames():
                if ftype == T_HELLO:
                    hello = json.loads(payload.decode())
                    break
                # ignore stray pre-HELLO frames
            if hello is not None:
                break

        job_id = hello.get("jobId", f"job{n}")
        entrypoint = hello.get("entrypoint", "main.py")
        files = hello.get("files", [])
        mem_bytes = int(hello.get("memMaxBytes") or DEFAULT_MEM_MAX)
        pids_max = int(hello.get("pidsMax") or DEFAULT_PIDS_MAX)
        tmpfs_bytes = int(hello.get("tmpfsBytes") or DEFAULT_TMPFS)
        # the HELLO may carry an explicit uid; otherwise UID_BASE + n
        if hello.get("uid") is not None:
            uid_n = int(hello["uid"]) - UID_BASE
            if uid_n < 0:
                uid_n = n
        else:
            uid_n = n

        # NOTE: files are NOT written to a shared disk dir. They are handed to the
        # child via fork() and materialized into the child's OWN private /tmp tmpfs
        # (see _materialize_files), so sibling children can never read them.

        # ---- 4 socketpairs: child stdin/stdout/stderr + artifact channel ----
        # The artifact pair carries workspace files the child streams back AFTER
        # user code (the workspace is in the child's private mount namespace, so
        # the agent cannot read it directly — see _capture_artifacts).
        p_in, c_in = socket.socketpair(socket.AF_UNIX, socket.SOCK_STREAM)
        p_out, c_out = socket.socketpair(socket.AF_UNIX, socket.SOCK_STREAM)
        p_err, c_err = socket.socketpair(socket.AF_UNIX, socket.SOCK_STREAM)
        p_art, c_art = socket.socketpair(socket.AF_UNIX, socket.SOCK_STREAM)
        # convert agent-side socketpair ends to raw fds we can select() on
        p_in_fd, c_in_fd = p_in.fileno(), c_in.fileno()
        p_out_fd, c_out_fd = p_out.fileno(), c_out.fileno()
        p_err_fd, c_err_fd = p_err.fileno(), c_err.fileno()
        c_art_fd = c_art.fileno()

        # ---- double-fork + harden the session (serialized: fork-in-thread) ----
        with _spawn_lock:
            realpid = spawn_session(files, entrypoint, uid_n, mem_bytes,
                                    tmpfs_bytes, c_in_fd, c_out_fd, c_err_fd,
                                    c_art_fd)
        # close the CHILD ends in the agent (child has its own copies)
        for s in (c_in, c_out, c_err, c_art):
            try:
                s.close()
            except OSError:
                pass
        c_in = c_out = c_err = c_art = None

        # ---- per-child cgroup leaf + placement ----
        leaf = make_cgroup_leaf(realpid, n, mem_bytes, pids_max)

        # ---- STARTED ----
        # cgroupEnforced is true ONLY when the per-child cgroup leaf with its
        # memory + pids limits was actually created on a REAL cgroupfs (v2 or v1)
        # AND the session pid placed in it (make_cgroup_leaf returns a leaf only
        # on full success). When false (e.g. Docker Desktop without delegated
        # cgroups) the Go side SKIPs the cgroup-enforcement abuse tests.
        cgroup_enforced = leaf is not None
        send_json(conn, T_STARTED,
                  {"realpid": realpid, "cgroupEnforced": cgroup_enforced})
        started = True
        log(f"job {job_id}: started realpid={realpid} cgroup={leaf['mode'] if leaf else 'no'}")

        # ---- relay loop ----
        (exit_code, exit_signal), artifacts = relay_loop(
            conn, reader, p_in, p_out, p_err, p_art, realpid, leaf)
        # Stream captured artifacts BEFORE the terminal EXIT frame so the worker's
        # relay reader has the full set by the time Wait (on EXIT) resolves.
        artifacts_truncated = _send_artifacts(conn, artifacts)
        exited = True
        send_json(conn, T_EXIT, {"exitCode": exit_code, "signal": exit_signal,
                                 "artifactsTruncated": artifacts_truncated})
    except (BrokenPipeError, ConnectionResetError):
        # The worker closed the relay first (kill, timeout, cleanup): the session
        # was killed and there is nobody left to send EXIT to. Routine, not an error.
        log(f"job {n}: relay closed by worker; session killed")
    except Exception as e:
        log(f"job {n} error: {e}")
        if started and not exited:
            try:
                send_json(conn, T_EXIT, {"exitCode": None, "signal": None,
                                         "error": str(e)})
            except Exception:
                pass
    finally:
        cleanup()
        try:
            conn.close()
        except OSError:
            pass


def reap(realpid):
    """Reap the session (reparented to us via subreaper). Returns
    (exit_code, signal) or None if not yet exited."""
    try:
        pid, status = os.waitpid(realpid, os.WNOHANG)
    except ChildProcessError:
        # already reaped elsewhere (shouldn't happen) — treat as exited
        return (None, None)
    except OSError:
        return None
    if pid == 0:
        return None
    if os.WIFEXITED(status):
        return (os.WEXITSTATUS(status), None)
    if os.WIFSIGNALED(status):
        return (None, os.WTERMSIG(status))
    return (None, None)


def relay_loop(conn, reader, p_in, p_out, p_err, p_art, realpid, leaf):
    """select() loop: conn STDIN->child stdin; STDIN_CLOSE->shutdown child stdin;
    KILL/conn-close->cgroup.kill; child stdout/stderr->STDOUT/STDERR frames;
    child artifact channel->accumulated (name, data) records; CPU push ~every
    100ms; on child reap (and all streams drained) return ((exit_code, signal),
    artifacts). Artifacts are accumulated here and framed by the caller AFTER the
    loop (with conn back in blocking mode) so a large file is sent reliably."""
    conn.setblocking(False)
    for s in (p_in, p_out, p_err, p_art):
        s.setblocking(False)
    conn_fd = conn.fileno()
    p_in_fd, p_out_fd, p_err_fd = p_in.fileno(), p_out.fileno(), p_err.fileno()
    p_art_fd = p_art.fileno()

    conn_open = True       # False once the worker closed its side (EOF/error)
    kill_sent_at = None    # monotonic time of the last kill_session()
    kill_attempts = 0

    def emit(ftype, payload):
        """Frame to the worker; a no-op once the connection is gone."""
        nonlocal conn_open
        if not conn_open:
            return
        try:
            send_frame(conn, ftype, payload)
        except OSError:
            conn_open = False

    out_open = True
    err_open = True
    art_open = True
    stdin_open = True
    last_cpu_push = 0.0
    last_cpu_ms = -1
    result = None
    art_reader = ArtifactReader()
    artifacts = []

    while True:
        # has the child exited?
        if result is None:
            r = reap(realpid)
            if r is not None:
                result = r
                # drain any remaining stdout/stderr/artifacts before returning
        rlist = [conn_fd] if conn_open else []
        if out_open:
            rlist.append(p_out_fd)
        if err_open:
            rlist.append(p_err_fd)
        if art_open:
            rlist.append(p_art_fd)

        if not rlist:
            # worker gone and every child stream drained: pace the reap poll
            time.sleep(0.05)
            ready = []
        else:
            try:
                ready, _, _ = select.select(rlist, [], [], 0.05)
            except (OSError, ValueError):
                # e.g. an fd >= FD_SETSIZE — never spin on a failing select()
                time.sleep(0.05)
                ready = []

        # --- worker -> agent frames ---
        if conn_open and conn_fd in ready:
            try:
                data = conn.recv(65536)
            except (BlockingIOError, InterruptedError):
                data = b"x"  # spurious wakeup; keep going
            except OSError:
                data = b""
            if data == b"":
                # conn closed by worker == implicit KILL. Stop selecting on the
                # dead socket: an EOF'd fd stays readable forever, so keeping it
                # in rlist turns this loop into a busy spin (one core per
                # orphaned job, observed in prod). Then wait for the child to be
                # reaped so we still return a real result.
                conn_open = False
                kill_session(leaf, realpid)
                kill_sent_at = time.monotonic()
            elif data != b"x":
                reader.feed(data)
                for ftype, payload in reader.iter_frames():
                    if ftype == T_STDIN and stdin_open:
                        try:
                            p_in.sendall(payload)
                        except OSError:
                            stdin_open = False
                    elif ftype == T_STDIN_CLOSE and stdin_open:
                        try:
                            p_in.shutdown(socket.SHUT_WR)
                        except OSError:
                            pass
                        stdin_open = False
                    elif ftype == T_KILL:
                        kill_session(leaf, realpid)
                        kill_sent_at = time.monotonic()

        # --- child stdout/stderr -> frames ---
        if out_open and p_out_fd in ready:
            try:
                chunk = p_out.recv(65536)
            except (BlockingIOError, InterruptedError):
                chunk = None
            except OSError:
                chunk = b""
            if chunk == b"":
                out_open = False
            elif chunk:
                emit(T_STDOUT, chunk)
        if err_open and p_err_fd in ready:
            try:
                chunk = p_err.recv(65536)
            except (BlockingIOError, InterruptedError):
                chunk = None
            except OSError:
                chunk = b""
            if chunk == b"":
                err_open = False
            elif chunk:
                emit(T_STDERR, chunk)

        # --- child artifact channel -> accumulated (name, data) records ---
        # Drained concurrently so a child blocked writing a large artifact (full
        # socket buffer) unblocks; EOF (child closed the fd before exit) ends it.
        if art_open and p_art_fd in ready:
            try:
                chunk = p_art.recv(65536)
            except (BlockingIOError, InterruptedError):
                chunk = None
            except OSError:
                chunk = b""
            if chunk == b"":
                art_open = False
            elif chunk:
                art_reader.feed(chunk)
                for name, data in art_reader.iter_records():
                    artifacts.append((name, data))

        # --- CPU push ~every 100ms ---
        now = time.monotonic()
        if now - last_cpu_push >= 0.1:
            last_cpu_push = now
            ms = cpu_ms(leaf, realpid)
            if ms is not None and ms != last_cpu_ms:
                last_cpu_ms = ms
                emit(T_CPU, json.dumps({"cpuMs": ms}).encode())

        # --- terminal condition: child reaped AND output + artifacts drained ---
        if result is not None and not out_open and not err_open and not art_open:
            # one final CPU sample
            ms = cpu_ms(leaf, realpid)
            if ms is not None and ms != last_cpu_ms:
                emit(T_CPU, json.dumps({"cpuMs": ms}).encode())
            return result, artifacts
        # if child reaped but pipes never EOF (e.g. inherited by a lingering
        # grandchild we already killed), bail after the cgroup is empty — or,
        # without a cgroup to consult, a few seconds after we killed it.
        if result is not None and leaf is not None and _cgroup_empty(leaf):
            return result, artifacts
        if result is not None and kill_sent_at is not None and now - kill_sent_at >= 5.0:
            return result, artifacts

        # --- a killed session that refuses to die (e.g. stuck in D-state):
        # re-send SIGKILL every 5s and log sparsely. This never spins: the worker
        # socket is no longer selected on once it hit EOF, so every pass blocks
        # on the child fds (or sleeps) before polling the reap again.
        if result is None and kill_sent_at is not None and now - kill_sent_at >= 5.0:
            kill_attempts += 1
            kill_sent_at = now
            kill_session(leaf, realpid)
            if kill_attempts in (1, 6, 60):
                log(f"session {realpid}: not reaped {kill_attempts * 5}s after kill, re-sent SIGKILL")


def _cgroup_empty(leaf):
    try:
        with open(_cgroup_procs_path(leaf)) as f:
            return f.read().strip() == ""
    except OSError:
        return True


# ============================ Accept loop ===================================
def main():
    global CG_LAYOUT
    # subreaper FIRST: so sessions whose intermediate exits reparent to us
    try:
        prctl(PR_SET_CHILD_SUBREAPER, 1)
    except OSError as e:
        log(f"PR_SET_CHILD_SUBREAPER: {e} (continuing)")

    do_preimport(preimport_set())
    CG_LAYOUT = setup_cgroup_base()

    srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind(("0.0.0.0", RELAY_PORT))
    srv.listen(64)
    log(f"listening on 0.0.0.0:{RELAY_PORT}")

    # Each job runs in its own thread and reaps its OWN session directly via
    # waitpid(realpid) (no shared generic reaper -> no waitpid race between jobs).
    # Intermediates are reaped inline by spawn_session. A thread crash never
    # takes the agent down.
    while True:
        try:
            conn, addr = srv.accept()
        except OSError as e:
            log(f"accept error: {e}")
            continue
        # one thread per job so a slow/looping job never blocks new accepts and
        # so each job's waitpid(realpid) stays isolated. Robust: a thread crash
        # never takes the agent down.
        t = threading.Thread(target=_job_thread, args=(conn,), daemon=True)
        t.start()


def _job_thread(conn):
    try:
        handle_job(conn)
    except Exception as e:
        log(f"job thread crashed: {e}")
        try:
            conn.close()
        except OSError:
            pass


if __name__ == "__main__":
    main()
