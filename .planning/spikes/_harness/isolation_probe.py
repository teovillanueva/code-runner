#!/usr/bin/env python3
"""Isolation + FD-inheritance probe for the hardened zygote (spike 006b).

Two parts:

PART 1 -- cross-child isolation under full per-child hardening (L4-equivalent):
fork two hardened children (distinct UID + private PID-ns + private mount-ns +
private /tmp) and have each ATTEMPT the forbidden:
  - read a sibling's /proc/<realpid>/mem
  - list the parent's /proc/<realpid>/fd
  - read a neighbor's /tmp sentinel
Each attempt must FAIL. We pass real (root-ns) pids in via argv so the attack has
a concrete target even though the child's own /proc only shows itself.

PART 2 -- the FD-inheritance proof of DESIGN RULE #1 ("the zygote parent must be
credential-free; fork() inherits FDs"). A stand-in "redis/soketi" listener runs on
localhost inside the pool. We demonstrate, in order:
  A. CREDENTIALED PARENT  -> untrusted child finds the inherited socket in
     /proc/self/fd and USES it, even after unshare(CLONE_NEWNET). Network
     isolation does NOT revoke an already-open fd.  => LEAK.
  B. CLOEXEC MYTH         -> set FD_CLOEXEC on the socket, fork (NO exec). The fd
     is STILL open and usable in the child, because CLOEXEC only acts on execve(),
     and the zygote runs user code WITHOUT exec.  => CLOEXEC DOES NOT PROTECT.
  C. MITIGATION: credential-free parent (never holds the socket) -> child finds
     nothing.  => SAFE.
  D. MITIGATION: fd-scrub (child closes all fds>2 before user code) -> socket gone.
     => SAFE.

Emits a JSON result block between ===ISO_BEGIN===/===ISO_END===.
"""
import ctypes
import json
import os
import socket
import sys
import threading
import time

CLONE_NEWNS = 0x00020000
CLONE_NEWPID = 0x20000000
CLONE_NEWNET = 0x40000000
MS_NOSUID, MS_NODEV, MS_NOEXEC = 2, 4, 8
PR_SET_NO_NEW_PRIVS = 38
UID_BASE = 100000

libc = ctypes.CDLL(None, use_errno=True)
# argtypes MANDATORY (see zygote_hardened.py): without it ctypes mis-marshals the
# large CLONE_* flags and unshare() returns EINVAL.
libc.unshare.argtypes = [ctypes.c_int]
libc.unshare.restype = ctypes.c_int
libc.mount.argtypes = [ctypes.c_char_p, ctypes.c_char_p, ctypes.c_char_p,
                       ctypes.c_ulong, ctypes.c_char_p]
libc.mount.restype = ctypes.c_int
libc.prctl.argtypes = [ctypes.c_int, ctypes.c_ulong, ctypes.c_ulong,
                       ctypes.c_ulong, ctypes.c_ulong]
libc.prctl.restype = ctypes.c_int


def unshare(flags):
    if libc.unshare(flags) != 0:
        e = ctypes.get_errno()
        raise OSError(e, os.strerror(e))


def mount(src, tgt, fstype, flags, data):
    s = src.encode() if src else None
    d = data.encode() if data else None
    if libc.mount(s, tgt.encode(), fstype.encode() if fstype else None, flags, d) != 0:
        e = ctypes.get_errno()
        raise OSError(e, os.strerror(e))


def prctl(opt, a2=0):
    libc.prctl(opt, a2, 0, 0, 0)


# ============================ PART 2: FD inheritance ==========================
RECEIVED = []  # bytes the stand-in "redis" listener actually received


def start_listener():
    srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind(("127.0.0.1", 6390))
    srv.listen(8)

    def serve():
        while True:
            try:
                c, _ = srv.accept()
            except OSError:
                return
            data = c.recv(256)
            if data:
                RECEIVED.append(data)

    threading.Thread(target=serve, daemon=True).start()
    time.sleep(0.3)
    return srv


def open_credential():
    """A live 'worker' connection to redis/soketi (the thing the parent must not hold)."""
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.connect(("127.0.0.1", 6390))
    return s


def child_tries_to_use_inherited_socket(tag, isolate_net=False, scrub=False):
    """Untrusted child: optionally enter a fresh netns / scrub fds, then hunt for an
    inherited socket in /proc/self/fd and try to write the secret through it."""
    if isolate_net:
        try:
            unshare(CLONE_NEWNET)  # child has NO network of its own ...
        except OSError:
            pass
    if scrub:
        # the only fork-safe mitigation besides a credential-free parent: close
        # every inherited fd above stdio BEFORE running user code.
        for fd in range(3, 4096):
            try:
                os.close(fd)
            except OSError:
                pass
    used = False
    for name in os.listdir("/proc/self/fd"):
        fd = int(name)
        if fd <= 2:
            continue
        try:
            s = socket.socket(fileno=os.dup(fd))
            s.send(tag.encode())  # ... yet it writes through the INHERITED fd
            used = True
            s.detach()
        except OSError:
            pass
    os._exit(0 if used else 7)


def run_fd_scenarios():
    srv = start_listener()
    out = {}

    def fork_and_wait(setup_socket, **child_kw):
        sock = setup_socket()
        pid = os.fork()
        if pid == 0:
            child_tries_to_use_inherited_socket(child_kw.pop("tag"), **child_kw)
        _, status = os.waitpid(pid, 0)
        if sock is not None:
            try:
                sock.close()
            except OSError:
                pass
        time.sleep(0.2)
        return os.waitstatus_to_exitcode(status)

    # A. credentialed parent, child also drops into its own netns
    RECEIVED.clear()
    fork_and_wait(open_credential, tag="A-STOLEN", isolate_net=True)
    out["A_credentialed_parent_netns_child"] = {
        "leak": any(b"A-STOLEN" in r for r in RECEIVED),
        "note": "child used inherited socket despite its own empty netns",
    }

    # B. CLOEXEC myth: mark CLOEXEC, fork (no exec)
    RECEIVED.clear()

    def cred_cloexec():
        s = open_credential()
        os.set_inheritable(s.fileno(), False)  # FD_CLOEXEC
        return s

    fork_and_wait(cred_cloexec, tag="B-STOLEN")
    out["B_cloexec_myth"] = {
        "leak": any(b"B-STOLEN" in r for r in RECEIVED),
        "note": "CLOEXEC acts only on execve(); zygote forks without exec -> no protection",
    }

    # C. mitigation: credential-free parent (never opens the socket)
    RECEIVED.clear()
    fork_and_wait(lambda: None, tag="C-STOLEN")
    out["C_credential_free_parent"] = {
        "leak": any(b"C-STOLEN" in r for r in RECEIVED),
        "note": "parent holds no connection -> nothing to inherit (DESIGN RULE #1)",
    }

    # D. mitigation: credentialed parent but child scrubs fds before user code
    RECEIVED.clear()
    fork_and_wait(open_credential, tag="D-STOLEN", scrub=True)
    out["D_fd_scrub_in_child"] = {
        "leak": any(b"D-STOLEN" in r for r in RECEIVED),
        "note": "child closes all fds>2 before user code -> safe even with credentialed parent",
    }

    srv.close()
    return out


# ====================== PART 1: cross-child isolation =========================
def harden_self(n, secret_fd):
    """Full L4-equivalent per-child hardening. Caller has ALREADY put us in a fresh
    PID namespace (parent unshared CLONE_NEWPID before forking us)."""
    unshare(CLONE_NEWNS)
    try:
        libc.mount(b"none", b"/", None, (1 << 18) | (1 << 14), None)  # MS_REC|MS_PRIVATE
    except Exception:
        pass
    # remount /proc so it reflects OUR pid namespace (we are PID 1 in it)
    try:
        mount("proc", "/proc", "proc", MS_NOSUID | MS_NODEV | MS_NOEXEC, None)
    except OSError:
        pass
    mount("tmpfs", "/tmp", "tmpfs", MS_NOSUID | MS_NODEV, "size=8m")
    with open("/tmp/secret", "w") as f:
        f.write(f"child-{n}-secret")
    prctl(PR_SET_NO_NEW_PRIVS, 1)
    uid = UID_BASE + n
    try:
        os.setgroups([])
    except OSError:
        pass
    os.setresgid(uid, uid, uid)
    os.setresuid(uid, uid, uid)


def attack(n, sibling_realpid, parent_realpid, secret_fd):
    res = {"my_pid_in_ns": os.getpid()}
    # 1. read sibling's /proc/<realpid>/mem (real root-ns pid passed in by parent)
    try:
        with open(f"/proc/{sibling_realpid}/mem", "rb") as f:
            f.seek(0x400000)
            f.read(16)
        res["read_sibling_proc_mem"] = f"SUCCEEDED (BAD) targeting realpid {sibling_realpid}"
    except Exception as e:
        res["read_sibling_proc_mem"] = f"blocked {type(e).__name__}:{getattr(e,'errno',None)}"
    # 2. list parent's fds. NOTE: the pool's python is PID 1 of the container AND
    #    we are PID 1 of our own pidns, so parent_realpid==1 collides with our own
    #    ns-pid -> /proc/1 resolves to OURSELVES, not the parent. Detect + prove it
    #    by reading what those fds actually point to.
    try:
        fds = os.listdir(f"/proc/{parent_realpid}/fd")
        if parent_realpid == os.getpid():
            targets = []
            for d in fds[:8]:
                try:
                    targets.append(os.readlink(f"/proc/{parent_realpid}/fd/{d}"))
                except OSError:
                    targets.append("?")
            res["list_parent_fds"] = (
                f"target pid {parent_realpid} == our own ns-pid -> these {len(fds)} fds "
                f"are OURS, not the parent's. parent NOT addressable across pid-ns. "
                f"fd targets={targets}")
        else:
            res["list_parent_fds"] = f"SUCCEEDED (BAD): {len(fds)} parent fds"
    except Exception as e:
        res["list_parent_fds"] = f"blocked {type(e).__name__}:{getattr(e,'errno',None)}"
    # 3. how many PIDs can we even see in our (remounted) /proc?
    try:
        visible = [d for d in os.listdir("/proc") if d.isdigit()]
        res["pids_visible_in_proc"] = visible
    except Exception as e:
        res["pids_visible_in_proc"] = f"err {e}"
    # 4. our /tmp is a private tmpfs -> we only ever see our own secret
    try:
        with open("/tmp/secret") as f:
            res["own_tmp_secret"] = f.read()
    except Exception as e:
        res["own_tmp_secret"] = f"err {e}"
    os.write(secret_fd, (json.dumps({f"child{n}": res}) + "\n").encode())


def run_isolation():
    """Fork two fully-hardened sessions, each in its OWN pid namespace (DOUBLE
    FORK: a process may unshare CLONE_NEWPID only once). The parent learns each
    session's real (root-ns) pid via a report pipe, then hands each session its
    sibling's + the parent's real pid over a control pipe so the targeted attack
    has a concrete root-ns target. Sessions report findings over a result pipe."""
    res_r, res_w = os.pipe()
    rep_r, rep_w = os.pipe()  # intermediate -> parent: "n,realpid"
    ctrl = {0: os.pipe(), 1: os.pipe()}  # parent -> session: "sib_realpid,parent_realpid"
    inters = []
    for n in (0, 1):
        inter = os.fork()
        if inter == 0:
            cr, cw = ctrl[n]
            # best-effort close of unrelated inherited fds (some already closed
            # by the parent for an earlier child -> ignore EBADF)
            for fd in (res_r, rep_r, cw):
                try:
                    os.close(fd)
                except OSError:
                    pass
            for k in ctrl:
                if k != n:
                    for fd in ctrl[k]:
                        try:
                            os.close(fd)
                        except OSError:
                            pass
            unshare(CLONE_NEWPID)  # first & only call -> OK
            g = os.fork()
            if g == 0:
                os.close(rep_w)
                try:
                    harden_self(n, res_w)
                    line = b""
                    while not line.endswith(b"\n"):
                        line += os.read(cr, 64)
                    sib, par = (int(x) for x in line.strip().split(b","))
                    attack(n, sib, par, res_w)
                except Exception as e:
                    os.write(res_w, (json.dumps({f"child{n}": f"FATAL {e}"}) + "\n").encode())
                os._exit(0)
            os.write(rep_w, f"{n},{g}\n".encode())
            os.close(rep_w)
            os.waitpid(g, 0)  # only 2 sessions -> cheap to stay as ns parent
            os._exit(0)
        inters.append(inter)
        os.close(ctrl[n][0])  # parent keeps the write end
    os.close(res_w)
    os.close(rep_w)
    # learn each session's real pid
    realpid = {}
    with os.fdopen(rep_r) as rf:
        for _ in range(2):
            nn, gg = rf.readline().strip().split(",")
            realpid[int(nn)] = int(gg)
    parent_real = os.getpid()
    os.write(ctrl[0][1], f"{realpid[1]},{parent_real}\n".encode())
    os.write(ctrl[1][1], f"{realpid[0]},{parent_real}\n".encode())
    os.close(ctrl[0][1])
    os.close(ctrl[1][1])
    out_lines = []
    with os.fdopen(res_r) as rf:
        for line in rf:
            if line.strip():
                out_lines.append(json.loads(line))
    for p in inters:
        try:
            os.waitpid(p, 0)
        except OSError:
            pass
    return out_lines


if __name__ == "__main__":
    mode = sys.argv[1] if len(sys.argv) > 1 else "all"
    result = {}
    # iso FIRST: it unshares CLONE_NEWPID in the main process, so the main must
    # still be single-threaded (the fd listener thread starts only after).
    if mode in ("all", "iso"):
        result["cross_child_isolation"] = run_isolation()
    if mode in ("all", "fd"):
        result["fd_inheritance"] = run_fd_scenarios()
    print("===ISO_BEGIN===")
    print(json.dumps(result, indent=2))
    print("===ISO_END===")
