#!/usr/bin/env python3
"""Hardened zygote / copy-on-write density probe (spike 006).

Spike 005 forked heavy children from a pre-imported parent with NO per-child
isolation (one container, pids 1024) and measured 30 -> 81 (2.7x), marginal
~41 MB/child. The open question for the real ZygoteRunner: does that gain SURVIVE
the per-child hardening the design mandates (rule #2)?

This probe ramps children from a parent that pre-imported the heavy stack, but
applies per-child hardening in LAYERS controlled by $LEVEL, so we can see which
layer (if any) costs density:

  LEVEL=0  plain fork (reproduces spike 005 on THIS box -> apples-to-apples baseline)
  LEVEL=1  + distinct UID per child  + PR_SET_NO_NEW_PRIVS
  LEVEL=2  + private mount namespace + private /tmp tmpfs + remounted /proc
  LEVEL=3  + per-child PID namespace (child is PID 1 of a fresh pidns)
  LEVEL=4  + per-child cgroup-v2 sub-cgroup (own memory.max + pids.max)

It ramps until host MemAvailable hits the same 220 MB floor spike 005 used, then
prints a single greppable marker line. Run once per LEVEL (pool container is
recreated each time) to get the layered curve.

The pool container must be PRIVILEGED (or hold CAP_SYS_ADMIN + CAP_SETUID/SETGID +
a writable cgroupfs) to create namespaces/cgroups for its children. That elevated
posture is itself a finding (see README) -- on Fly it is bounded by the Firecracker
microVM, which is the only boundary the threat model cares about. Privilege adds no
RAM, so the density numbers stay comparable across levels.
"""
import ctypes
import os
import sys
import time

# ---- Linux syscall constants -------------------------------------------------
CLONE_NEWNS = 0x00020000
CLONE_NEWPID = 0x20000000
MS_NOSUID = 2
MS_NODEV = 4
MS_NOEXEC = 8
PR_SET_NO_NEW_PRIVS = 38
PR_SET_PDEATHSIG = 1
SIGKILL = 9

libc = ctypes.CDLL(None, use_errno=True)
# Declaring argtypes/restype is MANDATORY: without it ctypes mis-marshals the
# large namespace flag constants (e.g. CLONE_NEWPID=0x20000000) and unshare()
# rejects them with EINVAL. (Measured the hard way on this box.)
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


# ---- Config ------------------------------------------------------------------
LEVEL = int(os.environ.get("LEVEL", "0"))
SAFETY_KB = int(os.environ.get("SAFETY_KB", "220000"))
HARD_CAP = int(os.environ.get("HARD_CAP", "260"))
UID_BASE = int(os.environ.get("UID_BASE", "100000"))
CHILD_MEM_MAX = os.environ.get("CHILD_MEM_MAX", str(128 * 1024 * 1024))  # 128 MB
CHILD_PIDS_MAX = os.environ.get("CHILD_PIDS_MAX", "64")
CGROOT = "/sys/fs/cgroup"
CG_BASE = None  # resolved at runtime if LEVEL>=4


def mem_avail_kb():
    with open("/proc/meminfo") as f:
        for line in f:
            if line.startswith("MemAvailable"):
                return int(line.split()[1])
    return 0


# ---- Pre-import the heavy stack ONCE (this is the shared CoW base) ------------
# Same set spike 005 used so the numbers are directly comparable. numpy.testing
# is fixed on the prod image now, so scipy/sklearn would also import -- but adding
# them here would change the base vs 005 and muddy the comparison. Keep parity.
import numpy as np  # noqa: E402
import pandas  # noqa: E402,F401
import matplotlib  # noqa: E402

matplotlib.use("Agg")
from matplotlib import pyplot  # noqa: E402,F401


def setup_cgroup_base():
    """Best-effort: build a delegated cgroup-v2 subtree for per-child leaves.

    Requires the pool to run --privileged --cgroupns=host (so /proc/self/cgroup
    resolves to a real, writable path). Returns the base dir for child leaves, or
    None if the kernel/cgroup layout refuses delegation (logged, non-fatal: the
    density conclusion does not depend on cgroup creation succeeding -- a sub-
    cgroup costs ~0 RAM. Blast-radius/OOM containment is verified separately).
    """
    try:
        with open("/proc/self/cgroup") as f:
            rel = f.read().strip().split("::", 1)[1]  # "0::/path"
        own = os.path.join(CGROOT, rel.lstrip("/"))
        base = os.path.join(own, "zygote")
        mgr = os.path.join(base, "mgr")
        os.makedirs(mgr, exist_ok=True)
        # move ourselves into the mgr leaf so `base` has no member procs
        with open(os.path.join(mgr, "cgroup.procs"), "w") as f:
            f.write(str(os.getpid()))
        # enable controllers for base's children
        with open(os.path.join(base, "cgroup.subtree_control"), "w") as f:
            f.write("+memory +pids")
        sys.stderr.write(f"[cg] delegated subtree at {base}\n")
        return base
    except Exception as e:
        sys.stderr.write(f"[cg] delegation unavailable ({e}); per-child cgroup skipped\n")
        return None


def place_in_cgroup(child_pid, n):
    if CG_BASE is None:
        return False
    try:
        leaf = os.path.join(CG_BASE, f"c{n}")
        os.makedirs(leaf, exist_ok=True)
        with open(os.path.join(leaf, "memory.max"), "w") as f:
            f.write(str(CHILD_MEM_MAX))
        with open(os.path.join(leaf, "pids.max"), "w") as f:
            f.write(str(CHILD_PIDS_MAX))
        with open(os.path.join(leaf, "cgroup.procs"), "w") as f:
            f.write(str(child_pid))
        return True
    except Exception as e:
        sys.stderr.write(f"[cg] place c{n} failed: {e}\n")
        return False


def child_body(n):
    """Runs in the forked (grand)child. Apply per-child hardening for the level,
    then allocate a UNIQUE ~40 MB working set and block (a held-open live session).

    NOTE: no PR_SET_PDEATHSIG here. Under LEVEL>=3 the session is forked by a thin
    intermediate that exits immediately (see spawn_session), so a pdeathsig would
    SIGKILL the session the instant the intermediate exits. Container teardown
    reaps everything at the end of the ramp anyway."""
    if LEVEL >= 2:
        # private mount ns + private /tmp + /proc reflecting our (pid)ns
        unshare(CLONE_NEWNS)
        # make / a private mount so our changes don't propagate to the parent
        try:
            mount("none", "/", None, 1 << 18 | 1 << 14, None)  # MS_REC|MS_PRIVATE
        except OSError:
            pass
        mount("tmpfs", "/tmp", "tmpfs", MS_NOSUID | MS_NODEV, "size=24m")
        try:
            mount("proc", "/proc", "proc", MS_NOSUID | MS_NODEV | MS_NOEXEC, None)
        except OSError as e:
            sys.stderr.write(f"[child {n}] proc remount: {e}\n")

    if LEVEL >= 1:
        prctl(PR_SET_NO_NEW_PRIVS, 1)
        uid = UID_BASE + n
        try:
            os.setgroups([])
        except OSError:
            pass
        os.setresgid(uid, uid, uid)
        os.setresuid(uid, uid, uid)

    # unique working set so the result is honest (not inflated by identical pages)
    rng = np.random.default_rng()
    buf = rng.random(5_000_000)  # ~40 MB unique anonymous memory
    buf[0] = 1.0
    while True:
        time.sleep(3600)


def _run_child(n):
    try:
        child_body(n)
    except Exception as e:  # pragma: no cover
        sys.stderr.write(f"[child {n}] FATAL {e}\n")
        os._exit(1)
    os._exit(0)


def spawn_session(n):
    """Fork one session; return its real (root-ns) pid for cgroup placement.

    LEVEL<3: a single fork (child runs the session directly).
    LEVEL>=3: DOUBLE FORK for a per-child PID namespace. A process may
    unshare(CLONE_NEWPID) only ONCE (the 2nd call EINVALs), so we fork a thin
    intermediate that unshares once, then forks the real session as PID 1 of a
    fresh pidns and exits. The session reparents to the pool's main process; the
    pidns lives as long as the session (its init) lives."""
    if LEVEL < 3:
        pid = os.fork()
        if pid == 0:
            _run_child(n)  # never returns
        return pid
    r, w = os.pipe()
    inter = os.fork()
    if inter == 0:
        os.close(r)
        unshare(CLONE_NEWPID)  # first & only call in this fresh process -> OK
        g = os.fork()
        if g == 0:
            os.close(w)
            _run_child(n)  # session = PID 1 of the new pidns
        os.write(w, f"{g}\n".encode())
        os.close(w)
        os._exit(0)  # intermediate exits; session reparents to main
    os.close(w)
    data = b""
    while not data.endswith(b"\n"):
        chunk = os.read(r, 32)
        if not chunk:
            break
        data += chunk
    os.close(r)
    return int(data.strip()) if data.strip() else None


def main():
    global CG_BASE
    if LEVEL >= 4:
        CG_BASE = setup_cgroup_base()

    n = 0
    base = mem_avail_kb()
    cg_ok = 0
    while n < HARD_CAP:
        if mem_avail_kb() < SAFETY_KB:
            break
        rp = spawn_session(n)
        if LEVEL >= 4 and rp and place_in_cgroup(rp, n):
            cg_ok += 1
        n += 1
        # reap exited intermediates (non-blocking) so pids don't leak; sleeping
        # session grandchildren are alive, so this never reaps them.
        try:
            while os.waitpid(-1, os.WNOHANG)[0] > 0:
                pass
        except OSError:
            pass
        time.sleep(2)  # let the session's buffer fault in to steady state

    after = mem_avail_kb()
    used = base - after
    per = used // n if n else 0
    sys.stderr.write(
        f"ZYGOTE_HARD level={LEVEL} ceiling={n} used_kb={used} "
        f"marginal_per_child_kb={per} base_kb={base} after_kb={after} cg_ok={cg_ok}\n"
    )
    sys.stderr.flush()
    time.sleep(15)  # hold so the harness can sample


if __name__ == "__main__":
    main()
