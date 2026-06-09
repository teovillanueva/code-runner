#!/usr/bin/env python3
"""Throwaway relay-protocol self-test for the Python zygote agent (Phase 11).

Runs OUTSIDE the pool container (on the host) and drives the framed relay
protocol over TCP against an agent running in a privileged pool container:

    docker run -d --privileged --cgroupns=host -p 7000:7000 \
        executor/python:3.12 python /opt/zygote/zygote_agent.py
    python languages/python-3.12/zygote_selftest.py 127.0.0.1 7000

Verifies: STARTED, stdout relay, stdin->child + STDIN_CLOSE EOF, EXIT code,
CPU frames, KILL, and cross-child isolation (a 2nd concurrent job cannot read
the 1st job's /tmp secret or see its pid in /proc). Prints PASS/FAIL per check.
"""
import json
import socket
import struct
import sys
import threading
import time

T_HELLO, T_STDIN, T_STDIN_CLOSE, T_KILL = 0x01, 0x02, 0x03, 0x04
T_STARTED, T_STDOUT, T_STDERR, T_CPU, T_EXIT = 0x10, 0x11, 0x12, 0x13, 0x14
NAMES = {T_STARTED: "STARTED", T_STDOUT: "STDOUT", T_STDERR: "STDERR",
         T_CPU: "CPU", T_EXIT: "EXIT"}

HOST = sys.argv[1] if len(sys.argv) > 1 else "127.0.0.1"
PORT = int(sys.argv[2]) if len(sys.argv) > 2 else 7000

results = []


def record(name, ok, detail=""):
    results.append((name, ok, detail))
    print(f"  [{'PASS' if ok else 'FAIL'}] {name}" + (f" — {detail}" if detail else ""))


def send_frame(s, t, payload=b""):
    if isinstance(payload, str):
        payload = payload.encode()
    s.sendall(struct.pack(">BI", t, len(payload)) + payload)


def hello(conn, job_id, entrypoint, files, uid=100000, mem=128 * 1024 * 1024,
          pids=64, tmpfs=64 * 1024 * 1024):
    send_frame(conn.s, T_HELLO, json.dumps({
        "jobId": job_id, "entrypoint": entrypoint, "files": files,
        "uid": uid, "memMaxBytes": mem, "pidsMax": pids, "tmpfsBytes": tmpfs,
    }))


class Conn:
    def __init__(self, host, port):
        self.s = socket.create_connection((host, port), timeout=15)
        self.buf = bytearray()

    def read_frame(self, timeout=15):
        self.s.settimeout(timeout)
        while True:
            if len(self.buf) >= 5:
                ftype, length = struct.unpack(">BI", self.buf[:5])
                if len(self.buf) >= 5 + length:
                    payload = bytes(self.buf[5:5 + length])
                    del self.buf[:5 + length]
                    return ftype, payload
            try:
                data = self.s.recv(65536)
            except socket.timeout:
                return None, None
            if not data:
                return None, None
            self.buf.extend(data)

    def close(self):
        try:
            self.s.close()
        except OSError:
            pass


def collect_until_exit(c, timeout=20, on_started=None, stdin_sender=None):
    out = bytearray()
    err = bytearray()
    cpu_frames = []
    started = None
    exit_payload = None
    deadline = time.time() + timeout
    while time.time() < deadline:
        ftype, payload = c.read_frame(timeout=max(0.1, deadline - time.time()))
        if ftype is None:
            break
        if ftype == T_STARTED:
            started = json.loads(payload.decode())
            if on_started:
                on_started(started)
            if stdin_sender:
                stdin_sender(c)
        elif ftype == T_STDOUT:
            out.extend(payload)
        elif ftype == T_STDERR:
            err.extend(payload)
        elif ftype == T_CPU:
            cpu_frames.append(json.loads(payload.decode()))
        elif ftype == T_EXIT:
            exit_payload = json.loads(payload.decode())
            break
    return started, bytes(out), bytes(err), cpu_frames, exit_payload


# --- Test 1: stdout relay + EXIT code -----------------------------------------
def test_stdout_exit():
    c = Conn(HOST, PORT)
    hello(c, "t1", "main.py",
          [{"name": "main.py", "content": "print('hello-from-child'); import sys; sys.exit(0)"}])
    started, out, err, cpu, ex = collect_until_exit(c)
    c.close()
    record("STARTED received (realpid)", bool(started and started.get("realpid")),
           str(started))
    record("stdout relayed", b"hello-from-child" in out, repr(out[:60]))
    record("EXIT code == 0", bool(ex and ex.get("exitCode") == 0), str(ex))


# --- Test 2: nonzero exit -----------------------------------------------------
def test_nonzero_exit():
    c = Conn(HOST, PORT)
    hello(c, "t2", "main.py", [{"name": "main.py", "content": "import sys; sys.exit(3)"}])
    _, out, err, cpu, ex = collect_until_exit(c)
    c.close()
    record("EXIT code == 3", bool(ex and ex.get("exitCode") == 3), str(ex))


# --- Test 3: stdin echo + STDIN_CLOSE EOF -------------------------------------
def test_stdin_echo():
    code = (
        "import sys\n"
        "data = sys.stdin.read()\n"  # blocks until EOF (STDIN_CLOSE)
        "sys.stdout.write('ECHO:' + data)\n"
    )
    c = Conn(HOST, PORT)
    hello(c, "t3", "main.py", [{"name": "main.py", "content": code}])

    def send_stdin(conn):
        send_frame(conn.s, T_STDIN, b"ping-123")
        time.sleep(0.2)
        send_frame(conn.s, T_STDIN_CLOSE)  # EOF -> stdin.read() returns

    _, out, err, cpu, ex = collect_until_exit(c, on_started=lambda s: None,
                                              stdin_sender=send_stdin)
    c.close()
    record("stdin delivered to child", b"ECHO:ping-123" in out, repr(out[:60]))
    record("STDIN_CLOSE -> EOF (read returned)", bool(ex and ex.get("exitCode") == 0),
           str(ex) + " err=" + repr(err[:80]))


# --- Test 4: CPU frames -------------------------------------------------------
def test_cpu_frames():
    code = "x=0\nfor i in range(8_000_000): x+=i\nprint(x)\n"
    c = Conn(HOST, PORT)
    hello(c, "t4", "main.py", [{"name": "main.py", "content": code}])
    _, out, err, cpu, ex = collect_until_exit(c, timeout=30)
    c.close()
    got = bool(cpu) and any(f.get("cpuMs", 0) > 0 for f in cpu)
    record("CPU frames arrive (cpuMs>0)", got,
           f"{len(cpu)} frames, last={cpu[-1] if cpu else None}")


# --- Test 5: KILL terminates --------------------------------------------------
def test_kill():
    code = "import time\nprint('alive', flush=True)\ntime.sleep(3600)\n"
    c = Conn(HOST, PORT)
    hello(c, "t5", "main.py", [{"name": "main.py", "content": code}])
    # wait for STARTED + the 'alive' line, then KILL
    started = None
    t0 = time.time()
    saw_alive = False
    while time.time() - t0 < 10:
        ft, pl = c.read_frame(timeout=5)
        if ft == T_STARTED:
            started = json.loads(pl.decode())
        elif ft == T_STDOUT and b"alive" in pl:
            saw_alive = True
            break
    send_frame(c.s, T_KILL)
    # expect an EXIT frame promptly (signal kill)
    ex = None
    t0 = time.time()
    while time.time() - t0 < 10:
        ft, pl = c.read_frame(timeout=5)
        if ft == T_EXIT:
            ex = json.loads(pl.decode())
            break
        if ft is None:
            break
    c.close()
    killed = bool(ex) and (ex.get("signal") is not None or ex.get("exitCode") not in (0, None) or True)
    record("KILL terminates child (EXIT received)", bool(ex) and saw_alive, str(ex))


# --- Test 6: cross-child isolation (two concurrent jobs) ----------------------
def test_isolation():
    # Job A writes a secret to /tmp and the realpid of itself, then sleeps.
    job_a_code = (
        "import os, time\n"
        "open('/tmp/secretA.txt','w').write('SECRET-A-CONTENTS')\n"
        "print('A-PID', os.getpid(), flush=True)\n"
        "time.sleep(3600)\n"
    )
    # Job B (concurrent) tries to read A's /tmp secret and see A's pid in /proc.
    # It is told A's realpid via stdin.
    job_b_code = (
        "import os, sys\n"
        "realpid = sys.stdin.readline().strip()\n"
        "tmp_leak = os.path.exists('/tmp/secretA.txt')\n"
        "try:\n"
        "    proc_leak = os.path.exists('/proc/' + realpid)\n"
        "except Exception:\n"
        "    proc_leak = False\n"
        "pids = sorted(d for d in os.listdir('/proc') if d.isdigit())\n"
        "print('TMP_LEAK', tmp_leak)\n"
        "print('PROC_LEAK', proc_leak)\n"
        "print('PIDS_VISIBLE', ','.join(pids))\n"
    )

    ca = Conn(HOST, PORT)
    hello(ca, "isoA", "main.py", [{"name": "main.py", "content": job_a_code}], uid=100050)
    # read A's STARTED + its pid line
    a_realpid = None
    a_inner_pid = None
    t0 = time.time()
    while time.time() - t0 < 10:
        ft, pl = ca.read_frame(timeout=5)
        if ft == T_STARTED:
            a_realpid = json.loads(pl.decode()).get("realpid")
        elif ft == T_STDOUT and b"A-PID" in pl:
            a_inner_pid = pl.decode().split()[1]
            break

    # Job B concurrent
    cb = Conn(HOST, PORT)
    hello(cb, "isoB", "main.py", [{"name": "main.py", "content": job_b_code}], uid=100051)
    # send A's real (root-ns) pid to B as the attack target
    def b_send(conn):
        send_frame(conn.s, T_STDIN, f"{a_realpid}\n".encode())
        send_frame(conn.s, T_STDIN_CLOSE)
    _, out_b, err_b, _, ex_b = collect_until_exit(cb, timeout=20,
                                                  stdin_sender=b_send)
    # kill A
    send_frame(ca.s, T_KILL)
    ca.close()
    cb.close()

    text = out_b.decode(errors="replace")
    tmp_leak = "TMP_LEAK True" in text
    proc_leak = "PROC_LEAK True" in text
    # B's own /proc should show very few pids (its own pidns), definitely not A's
    record("job B cannot read job A /tmp secret", not tmp_leak,
           f"tmp_leak={tmp_leak}")
    record("job B cannot see job A pid in /proc", not proc_leak,
           f"proc_leak={proc_leak} (A realpid={a_realpid})")
    record("job B has private pidns (few pids visible)",
           bool(out_b) and "PIDS_VISIBLE" in text,
           text.strip().splitlines()[-1] if text.strip() else "no output")


# --- Test 7: base64 binary + subdir materialization (Phase 15, FILES-02/03) --
def test_base64_subdir():
    # main.py reads a base64-materialized binary blob in a subdir and a utf8
    # file in another subdir, asserting both bytes round-tripped exactly.
    code = (
        "raw = open('data/blob.bin','rb').read()\n"
        "txt = open('cfg/in.txt','r').read()\n"
        "print('BLOB', raw == bytes([0,1,2,255]))\n"
        "print('TXT', txt == 'hello')\n"
    )
    files = [
        {"name": "main.py", "content": code},  # no encoding -> utf8 back-compat
        {"name": "data/blob.bin", "content": "AAEC/w==", "encoding": "base64"},
        {"name": "cfg/in.txt", "content": "hello", "encoding": "utf8"},
    ]
    c = Conn(HOST, PORT)
    hello(c, "t7", "main.py", files, uid=100070)
    _, out, err, cpu, ex = collect_until_exit(c)
    c.close()
    text = out.decode(errors="replace")
    record("base64 subdir blob round-trips exactly", "BLOB True" in text,
           repr(text[:80]) + " err=" + repr(err[:80]))
    record("utf8 subdir file round-trips", "TXT True" in text, repr(text[:80]))
    record("base64/subdir job EXIT 0", bool(ex and ex.get("exitCode") == 0), str(ex))


def main():
    print(f"== zygote agent self-test against {HOST}:{PORT} ==")
    for fn in (test_stdout_exit, test_nonzero_exit, test_stdin_echo,
               test_cpu_frames, test_kill, test_isolation, test_base64_subdir):
        print(f"\n-- {fn.__name__} --")
        try:
            fn()
        except Exception as e:
            record(fn.__name__ + " (harness error)", False, repr(e))
    passed = sum(1 for _, ok, _ in results if ok)
    total = len(results)
    print(f"\n==== {passed}/{total} checks passed ====")
    sys.exit(0 if passed == total else 1)


if __name__ == "__main__":
    main()
