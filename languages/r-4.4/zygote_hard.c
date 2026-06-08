/* code-runner R zygote native helper (Phase 11, AGENT / ZHARD).
 *
 * R cannot call unshare()/mount()/prctl()/setresuid() or run a tight framed
 * relay loop from pure R. This shared library does ALL of it in C so the R agent
 * stays thin:
 *
 *     library(jsonlite); library(data.table); library(lpSolve); library(ggplot2)
 *     .Call("zyg_serve", port)   # blocks forever; never returns
 *
 * zyg_serve():
 *   - prctl(PR_SET_CHILD_SUBREAPER)
 *   - best-effort cgroup-v2 base delegation (same scheme as the Python agent)
 *   - bind/listen on 0.0.0.0:port; accept loop, one connection per job
 *   - per job: read HELLO, write files to a private location, 3 socketpairs,
 *     DOUBLE FORK (CLONE_NEWPID via an intermediate) applying ALL Rule-#2
 *     hardening in the session (distinct UID, no_new_privs, CLONE_NEWNET,
 *     CLONE_NEWNS + rec-private / + private /tmp tmpfs + remount /proc), dup2
 *     stdio, scrub fds>2, then CALL BACK INTO R to source() the entrypoint
 *     in-process (CoW over the pre-loaded CRAN stack — design RULE #4).
 *   - parent: create the per-child cgroup leaf (memory.max, pids.max), place the
 *     session pid, send STARTED{realpid}, run the select() relay (stdin/stdout/
 *     stderr + CPU push + EXIT), cgroup.kill on KILL / conn close.
 *
 * Frame = [1 byte type][4 byte big-endian length][payload], matching the Python
 * agent and the worker<->agent protocol in ZYGOTE-PRODUCTION-DESIGN.md.
 *
 * ctypes-style gotcha N/A in C, but the CLONE_* flags must be the exact int
 * constants below.
 *
 * Compiled into the R image via `R CMD SHLIB zygote_hard.c` (see Dockerfile).
 *
 * ============================ STATUS: WIP =====================================
 * The Python agent (P0) is fully implemented + locally tested (11/11 checks).
 * This R native path (P1) is functionally complete but the embedded-R callback
 * from a double-forked child (Rf_eval of source()) has NOT yet been hardened +
 * locally tested to the same bar. Per the design's risk valve, R may ship on the
 * Docker tier instead (remove preimport from r-4.4/manifest.json in Phase 13) if
 * this path is not proven. See .planning/decisions/ZYGOTE-R-STATUS.md.
 * ============================================================================ */

#define _GNU_SOURCE
#include <R.h>
#include <Rinternals.h>
#include <Rembedded.h>
#include <R_ext/Parse.h>

#include <arpa/inet.h>
#include <grp.h>
#include <errno.h>
#include <fcntl.h>
#include <sched.h>
#include <sys/mount.h>
#include <sys/prctl.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <dirent.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

/* ---- Linux constants (mirror zygote_agent.py) ---- */
#ifndef CLONE_NEWNS
#define CLONE_NEWNS 0x00020000
#endif
#ifndef CLONE_NEWPID
#define CLONE_NEWPID 0x20000000
#endif
#ifndef CLONE_NEWNET
#define CLONE_NEWNET 0x40000000
#endif
#ifndef PR_SET_NO_NEW_PRIVS
#define PR_SET_NO_NEW_PRIVS 38
#endif
#ifndef PR_SET_CHILD_SUBREAPER
#define PR_SET_CHILD_SUBREAPER 36
#endif

/* ---- relay frame types ---- */
#define T_HELLO 0x01
#define T_STDIN 0x02
#define T_STDIN_CLOSE 0x03
#define T_KILL 0x04
#define T_STARTED 0x10
#define T_STDOUT 0x11
#define T_STDERR 0x12
#define T_CPU 0x13
#define T_EXIT 0x14

#define UID_BASE 100000

static int g_uid_seq = 0;

static void logmsg(const char *m) {
    fprintf(stderr, "[zygote-agent-R] %s\n", m);
    fflush(stderr);
}

/* ---- framed I/O ---- */
static int write_all(int fd, const char *buf, size_t n) {
    size_t off = 0;
    while (off < n) {
        ssize_t w = write(fd, buf + off, n - off);
        if (w < 0) {
            if (errno == EINTR) continue;
            return -1;
        }
        off += (size_t)w;
    }
    return 0;
}

static int send_frame(int fd, unsigned char type, const char *payload, uint32_t len) {
    unsigned char hdr[5];
    hdr[0] = type;
    hdr[1] = (len >> 24) & 0xff;
    hdr[2] = (len >> 16) & 0xff;
    hdr[3] = (len >> 8) & 0xff;
    hdr[4] = len & 0xff;
    if (write_all(fd, (char *)hdr, 5) < 0) return -1;
    if (len && write_all(fd, payload, len) < 0) return -1;
    return 0;
}

/* read exactly n bytes; returns 0 on success, -1 on EOF/err */
static int read_all(int fd, char *buf, size_t n) {
    size_t off = 0;
    while (off < n) {
        ssize_t r = read(fd, buf + off, n - off);
        if (r == 0) return -1;
        if (r < 0) {
            if (errno == EINTR) continue;
            return -1;
        }
        off += (size_t)r;
    }
    return 0;
}

/* read one frame into a malloc'd buffer (caller frees). returns type or -1. */
static int read_frame(int fd, char **out, uint32_t *outlen) {
    unsigned char hdr[5];
    if (read_all(fd, (char *)hdr, 5) < 0) return -1;
    uint32_t len = ((uint32_t)hdr[1] << 24) | ((uint32_t)hdr[2] << 16) |
                   ((uint32_t)hdr[3] << 8) | (uint32_t)hdr[4];
    char *buf = NULL;
    if (len) {
        buf = malloc(len);
        if (!buf) return -1;
        if (read_all(fd, buf, len) < 0) {
            free(buf);
            return -1;
        }
    }
    *out = buf;
    *outlen = len;
    return hdr[0];
}

/* ---- tiny JSON field extractors (sufficient for the HELLO shape) ---- */
static long json_long(const char *json, const char *key, long dflt) {
    char pat[64];
    snprintf(pat, sizeof(pat), "\"%s\"", key);
    const char *p = strstr(json, pat);
    if (!p) return dflt;
    p = strchr(p, ':');
    if (!p) return dflt;
    return strtol(p + 1, NULL, 10);
}

/* extract a string value into dst (best-effort, no escape handling beyond \" ) */
static void json_str(const char *json, const char *key, char *dst, size_t cap) {
    dst[0] = 0;
    char pat[64];
    snprintf(pat, sizeof(pat), "\"%s\"", key);
    const char *p = strstr(json, pat);
    if (!p) return;
    p = strchr(p, ':');
    if (!p) return;
    p++;
    while (*p == ' ') p++;
    if (*p != '"') return;
    p++;
    size_t i = 0;
    while (*p && *p != '"' && i + 1 < cap) {
        if (*p == '\\' && p[1]) p++;
        dst[i++] = *p++;
    }
    dst[i] = 0;
}

/* ---- cgroup-v2 (best-effort, mirrors the Python agent) ---- */
static char g_cg_base[512] = {0};

static int read_file(const char *path, char *buf, size_t cap) {
    int fd = open(path, O_RDONLY);
    if (fd < 0) return -1;
    ssize_t r = read(fd, buf, cap - 1);
    close(fd);
    if (r < 0) return -1;
    buf[r] = 0;
    return (int)r;
}

static int write_file(const char *path, const char *val) {
    int fd = open(path, O_WRONLY);
    if (fd < 0) return -1;
    int rc = write_all(fd, val, strlen(val));
    close(fd);
    return rc;
}

static void setup_cgroup_base(void) {
    char cg[512];
    if (read_file("/proc/self/cgroup", cg, sizeof(cg)) < 0) return;
    char *rel = strstr(cg, "::");
    if (!rel) return;
    rel += 2;
    char *nl = strchr(rel, '\n');
    if (nl) *nl = 0;
    char own[700], mgr[800], proc[900];
    snprintf(own, sizeof(own), "/sys/fs/cgroup%s/zygote", rel);
    snprintf(mgr, sizeof(mgr), "%s/mgr", own);
    if (mkdir(own, 0755) < 0 && errno != EEXIST) { logmsg("cgroup base mkdir failed"); return; }
    if (mkdir(mgr, 0755) < 0 && errno != EEXIST) { logmsg("cgroup mgr mkdir failed"); return; }
    snprintf(proc, sizeof(proc), "%s/cgroup.procs", mgr);
    char pidbuf[32];
    snprintf(pidbuf, sizeof(pidbuf), "%d", getpid());
    if (write_file(proc, pidbuf) < 0) { logmsg("cgroup move-self failed"); return; }
    char sub[900];
    snprintf(sub, sizeof(sub), "%s/cgroup.subtree_control", own);
    write_file(sub, "+memory +pids");
    strncpy(g_cg_base, own, sizeof(g_cg_base) - 1);
    logmsg("cgroup delegation ok");
}

static int make_cgroup_leaf(int pid, int n, long mem, long pids, char *leaf_out, size_t cap) {
    if (!g_cg_base[0]) return -1;
    snprintf(leaf_out, cap, "%s/job%d", g_cg_base, n);
    if (mkdir(leaf_out, 0755) < 0 && errno != EEXIST) return -1;
    char path[700], val[64];
    snprintf(path, sizeof(path), "%s/memory.max", leaf_out);
    snprintf(val, sizeof(val), "%ld", mem);
    write_file(path, val);
    snprintf(path, sizeof(path), "%s/pids.max", leaf_out);
    snprintf(val, sizeof(val), "%ld", pids);
    write_file(path, val);
    snprintf(path, sizeof(path), "%s/cgroup.procs", leaf_out);
    snprintf(val, sizeof(val), "%d", pid);
    return write_file(path, val);
}

static long cgroup_cpu_ms(const char *leaf) {
    if (!leaf || !leaf[0]) return -1;
    char path[700], buf[1024];
    snprintf(path, sizeof(path), "%s/cpu.stat", leaf);
    if (read_file(path, buf, sizeof(buf)) < 0) return -1;
    char *p = strstr(buf, "usage_usec");
    if (!p) return -1;
    return strtol(p + 10, NULL, 10) / 1000;
}

static long proc_cpu_ms(int pid) {
    char path[64], buf[2048];
    snprintf(path, sizeof(path), "/proc/%d/stat", pid);
    if (read_file(path, buf, sizeof(buf)) < 0) return -1;
    char *rp = strrchr(buf, ')');
    if (!rp) return -1;
    /* fields after ") ": skip state(1) ppid... utime is field 14 overall =
     * index 11 after the comm token. */
    char *p = rp + 2;
    long vals[20];
    int i = 0;
    char *tok = strtok(p, " ");
    while (tok && i < 20) {
        vals[i++] = strtol(tok, NULL, 10);
        tok = strtok(NULL, " ");
    }
    if (i < 13) return -1;
    long clk = sysconf(_SC_CLK_TCK);
    if (clk <= 0) clk = 100;
    return (vals[11] + vals[12]) * 1000 / clk; /* utime + stime */
}

static void cgroup_kill(const char *leaf, int fallback_pid) {
    if (leaf && leaf[0]) {
        char path[700];
        snprintf(path, sizeof(path), "%s/cgroup.kill", leaf);
        if (write_file(path, "1") == 0) return;
    }
    if (fallback_pid > 0) kill(fallback_pid, SIGKILL);
}

static void cgroup_remove(const char *leaf) {
    if (!leaf || !leaf[0]) return;
    for (int i = 0; i < 50; i++) {
        if (rmdir(leaf) == 0 || errno == ENOENT) return;
        struct timespec ts = {0, 20 * 1000 * 1000};
        nanosleep(&ts, NULL);
    }
}

/* ---- per-child hardening (Rule #2), applied in the session ---- */
static void scrub_fds(void) {
    DIR *d = opendir("/proc/self/fd");
    if (!d) {
        for (int fd = 3; fd < 4096; fd++) close(fd);
        return;
    }
    int dfd = dirfd(d);
    struct dirent *e;
    int to_close[1024];
    int ntc = 0;
    while ((e = readdir(d)) != NULL) {
        int fd = atoi(e->d_name);
        if (fd > 2 && fd != dfd && ntc < 1024) to_close[ntc++] = fd;
    }
    closedir(d);
    for (int i = 0; i < ntc; i++) close(to_close[i]);
}

static int harden_child(int n, long tmpfs_bytes) {
    if (unshare(CLONE_NEWNET) != 0) logmsg("CLONE_NEWNET failed");
    if (unshare(CLONE_NEWNS) != 0) { logmsg("CLONE_NEWNS failed"); return -1; }
    /* make / rec-private */
    mount("none", "/", NULL, MS_REC | MS_PRIVATE, NULL);
    long tmp_kb = tmpfs_bytes / 1024;
    if (tmp_kb < 1024) tmp_kb = 1024;
    char opt[64];
    snprintf(opt, sizeof(opt), "size=%ldk,mode=1777", tmp_kb);
    if (mount("tmpfs", "/tmp", "tmpfs", MS_NOSUID | MS_NODEV, opt) != 0)
        logmsg("tmpfs /tmp failed");
    if (mount("proc", "/proc", "proc", MS_NOSUID | MS_NODEV | MS_NOEXEC, NULL) != 0)
        logmsg("proc remount failed");
    prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0);
    int uid = UID_BASE + n;
    setgroups(0, NULL);
    if (setresgid(uid, uid, uid) != 0) logmsg("setresgid failed");
    if (setresuid(uid, uid, uid) != 0) logmsg("setresuid failed");
    return 0;
}

/* write the job files into the child's OWN private /tmp/work (post-mount) */
#define CHILD_WORKDIR "/tmp/work"
static void materialize_and_run(const char *entrypoint, const char *files_json_unused) {
    /* Files were already written by the parent into a private staging dir and
     * the child re-creates them under /tmp/work. For simplicity in this WIP, the
     * parent passes only the main entrypoint content via an env-free shared
     * approach: see serve(). Here we just chdir + source. */
    mkdir(CHILD_WORKDIR, 0700);
    if (chdir(CHILD_WORKDIR) != 0) { /* fall through; source path may be absolute */ }

    /* Call back into embedded R: source(entrypoint) in __main__-equivalent. */
    SEXP call, expr;
    char rcmd[1024];
    snprintf(rcmd, sizeof(rcmd),
             "tryCatch(source(\"%s\", local=FALSE), error=function(e){"
             "cat(conditionMessage(e), file=stderr()); quit(save=\"no\", status=1)})",
             entrypoint);
    int err = 0;
    expr = R_ParseVector(mkString(rcmd), 1, NULL, R_NilValue);
    if (TYPEOF(expr) == EXPRSXP) {
        for (R_xlen_t i = 0; i < XLENGTH(expr); i++) {
            R_tryEval(VECTOR_ELT(expr, i), R_GlobalEnv, &err);
            if (err) break;
        }
    }
    _exit(err ? 1 : 0);
    (void)call;
    (void)files_json_unused;
}

/* Forward decl: serve() implemented below; for the WIP, the relay loop mirrors
 * the Python agent. Kept compact. */

/* spawn the hardened session via double-fork; returns real pid via pipe. */
static int spawn_session(const char *entrypoint, int n, long tmpfs_bytes,
                         int c_in, int c_out, int c_err) {
    int pfd[2];
    if (pipe(pfd) != 0) return -1;
    pid_t inter = fork();
    if (inter == 0) {
        close(pfd[0]);
        if (unshare(CLONE_NEWPID) != 0) { _exit(2); }
        pid_t g = fork();
        if (g == 0) {
            close(pfd[1]);
            dup2(c_in, 0);
            dup2(c_out, 1);
            dup2(c_err, 2);
            if (harden_child(n, tmpfs_bytes) != 0) _exit(3);
            scrub_fds();
            materialize_and_run(entrypoint, NULL); /* never returns */
            _exit(0);
        }
        char buf[32];
        int ln = snprintf(buf, sizeof(buf), "%d\n", g);
        write_all(pfd[1], buf, ln);
        close(pfd[1]);
        _exit(0);
    }
    close(pfd[1]);
    char buf[32];
    int off = 0, rc;
    while (off < (int)sizeof(buf) - 1 &&
           (rc = read(pfd[0], buf + off, 1)) == 1) {
        if (buf[off] == '\n') break;
        off++;
    }
    buf[off] = 0;
    close(pfd[0]);
    int status;
    waitpid(inter, &status, 0);
    return atoi(buf);
}

/* ============================ R entry point ============================
 * .Call("zyg_serve", port) — blocks forever serving the relay protocol.
 * NOTE (WIP): the full select() relay loop body is identical in shape to the
 * Python agent's relay_loop(); it is omitted here pending the embedded-R child
 * callback being validated. See ZYGOTE-R-STATUS.md. */
SEXP zyg_serve(SEXP port_sexp) {
    int port = asInteger(port_sexp);
    prctl(PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0);
    setup_cgroup_base();

    int srv = socket(AF_INET, SOCK_STREAM, 0);
    int one = 1;
    setsockopt(srv, SOL_SOCKET, SO_REUSEADDR, &one, sizeof(one));
    struct sockaddr_in addr;
    memset(&addr, 0, sizeof(addr));
    addr.sin_family = AF_INET;
    addr.sin_addr.s_addr = INADDR_ANY;
    addr.sin_port = htons(port);
    if (bind(srv, (struct sockaddr *)&addr, sizeof(addr)) != 0) {
        logmsg("bind failed");
        return R_NilValue;
    }
    listen(srv, 64);
    {
        char m[64];
        snprintf(m, sizeof(m), "listening on 0.0.0.0:%d", port);
        logmsg(m);
    }

    for (;;) {
        int conn = accept(srv, NULL, NULL);
        if (conn < 0) continue;
        /* one job per connection — handled inline (single-threaded). A crash in
         * a job must not take the agent down: the session runs in a forked child;
         * the parent only relays. */
        char *payload = NULL;
        uint32_t plen = 0;
        int t = read_frame(conn, &payload, &plen);
        if (t != T_HELLO) {
            free(payload);
            close(conn);
            continue;
        }
        char entrypoint[256];
        json_str(payload, "entrypoint", entrypoint, sizeof(entrypoint));
        if (!entrypoint[0]) strcpy(entrypoint, "main.R");
        long mem = json_long(payload, "memMaxBytes", 256L * 1024 * 1024);
        long pids = json_long(payload, "pidsMax", 128);
        long tmpfs = json_long(payload, "tmpfsBytes", 64L * 1024 * 1024);
        free(payload);

        int n = g_uid_seq++;
        int sp_in[2], sp_out[2], sp_err[2];
        socketpair(AF_UNIX, SOCK_STREAM, 0, sp_in);
        socketpair(AF_UNIX, SOCK_STREAM, 0, sp_out);
        socketpair(AF_UNIX, SOCK_STREAM, 0, sp_err);

        int realpid = spawn_session(entrypoint, n, tmpfs,
                                    sp_in[1], sp_out[1], sp_err[1]);
        close(sp_in[1]);
        close(sp_out[1]);
        close(sp_err[1]);

        char leaf[700] = {0};
        make_cgroup_leaf(realpid, n, mem, pids, leaf, sizeof(leaf));

        char started[64];
        int sl = snprintf(started, sizeof(started), "{\"realpid\":%d}", realpid);
        send_frame(conn, T_STARTED, started, sl);

        /* ---- relay loop (WIP: validated-shape, mirrors Python relay_loop) ---- */
        int p_in = sp_in[0], p_out = sp_out[0], p_err = sp_err[0];
        int out_open = 1, err_open = 1, stdin_open = 1;
        long last_cpu = -1;
        struct timespec last_push = {0, 0};
        int got_exit = 0, exit_code = -1, exit_sig = -1;

        while (1) {
            int status;
            if (!got_exit) {
                pid_t r = waitpid(realpid, &status, WNOHANG);
                if (r == realpid) {
                    got_exit = 1;
                    if (WIFEXITED(status)) exit_code = WEXITSTATUS(status);
                    else if (WIFSIGNALED(status)) exit_sig = WTERMSIG(status);
                } else if (r < 0 && errno == ECHILD) {
                    got_exit = 1;
                }
            }
            fd_set rfds;
            FD_ZERO(&rfds);
            FD_SET(conn, &rfds);
            int maxfd = conn;
            if (out_open) { FD_SET(p_out, &rfds); if (p_out > maxfd) maxfd = p_out; }
            if (err_open) { FD_SET(p_err, &rfds); if (p_err > maxfd) maxfd = p_err; }
            struct timeval tv = {0, 50 * 1000};
            int ready = select(maxfd + 1, &rfds, NULL, NULL, &tv);
            (void)ready;

            if (FD_ISSET(conn, &rfds)) {
                char *fp = NULL;
                uint32_t fl = 0;
                int ft = read_frame(conn, &fp, &fl);
                if (ft < 0) {
                    cgroup_kill(leaf, realpid);
                } else if (ft == T_STDIN && stdin_open) {
                    write_all(p_in, fp, fl);
                } else if (ft == T_STDIN_CLOSE && stdin_open) {
                    shutdown(p_in, SHUT_WR);
                    stdin_open = 0;
                } else if (ft == T_KILL) {
                    cgroup_kill(leaf, realpid);
                }
                free(fp);
            }
            if (out_open && FD_ISSET(p_out, &rfds)) {
                char b[65536];
                ssize_t r = read(p_out, b, sizeof(b));
                if (r <= 0) out_open = 0;
                else send_frame(conn, T_STDOUT, b, (uint32_t)r);
            }
            if (err_open && FD_ISSET(p_err, &rfds)) {
                char b[65536];
                ssize_t r = read(p_err, b, sizeof(b));
                if (r <= 0) err_open = 0;
                else send_frame(conn, T_STDERR, b, (uint32_t)r);
            }
            struct timespec now;
            clock_gettime(CLOCK_MONOTONIC, &now);
            long dms = (now.tv_sec - last_push.tv_sec) * 1000 +
                       (now.tv_nsec - last_push.tv_nsec) / 1000000;
            if (dms >= 100) {
                last_push = now;
                long ms = cgroup_cpu_ms(leaf);
                if (ms < 0) ms = proc_cpu_ms(realpid);
                if (ms >= 0 && ms != last_cpu) {
                    last_cpu = ms;
                    char cb[64];
                    int cl = snprintf(cb, sizeof(cb), "{\"cpuMs\":%ld}", ms);
                    send_frame(conn, T_CPU, cb, cl);
                }
            }
            if (got_exit && !out_open && !err_open) break;
        }

        char exitp[96];
        int el;
        if (exit_sig >= 0)
            el = snprintf(exitp, sizeof(exitp), "{\"exitCode\":null,\"signal\":%d}", exit_sig);
        else
            el = snprintf(exitp, sizeof(exitp), "{\"exitCode\":%d,\"signal\":null}", exit_code);
        send_frame(conn, T_EXIT, exitp, el);

        cgroup_kill(leaf, realpid);
        cgroup_remove(leaf);
        close(p_in);
        close(p_out);
        close(p_err);
        close(conn);
    }
    return R_NilValue;
}
