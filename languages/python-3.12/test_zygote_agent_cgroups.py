"""Unit tests for the zygote agent's cgroup layout detection and leaf handling.

Pure stdlib, no container needed: the agent module is loaded from this
directory and its probes (mount_entry, read_proc_cgroup, mount) are patched so
a temp dir can stand in for /sys/fs/cgroup. Run with:

    python3 -m unittest discover -s languages/python-3.12 -p 'test_*.py' -v

The "tmpfs" test pins the production bug this guards against: on Fly the pool
container sees /sys/fs/cgroup as a tmpfs with cgroup-v1 controller mounts, and
the agent used to "succeed" at creating v2 leaves there (plain directories), so
no limit was enforced and cgroup.kill killed nothing.
"""
import importlib.util
import os
import sys
import tempfile
import unittest
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location("zygote_agent", os.path.join(HERE, "zygote_agent.py"))
za = importlib.util.module_from_spec(_spec)
sys.modules["zygote_agent"] = za
_spec.loader.exec_module(za)

CID = "/docker/abc"

# /proc/self/cgroup of the pool container on a Fly worker (cgroup-v1 hybrid host).
FLY_HYBRID_PROCS = {
    10: (("cpuset",), CID),
    8: (("memory",), CID),
    5: (("cpu", "cpuacct"), CID),
    3: (("pids",), CID),
    0: ((), CID),
}

# /proc/self/mountinfo excerpt of that same pool container (paths rebased in the
# test). Note the ROOT field: Docker mounts each v1 controller with the
# container's own cgroup as the mount root.
FLY_POOL_MOUNTINFO = """\
600 580 0:150 / {root} rw,relatime - tmpfs tmpfs rw,mode=755
601 600 0:27 /docker/abc {root}/net_cls,net_prio rw,nosuid,nodev,noexec,relatime master:9 - cgroup cgroup rw,net_cls,net_prio
602 600 0:28 /docker/abc {root}/memory rw,nosuid,nodev,noexec,relatime master:10 - cgroup cgroup rw,memory
603 600 0:29 /docker/abc {root}/pids rw,nosuid,nodev,noexec,relatime master:11 - cgroup cgroup rw,pids
604 600 0:30 /docker/abc {root}/cpu,cpuacct rw,nosuid,nodev,noexec,relatime master:12 - cgroup cgroup rw,cpu,cpuacct
605 580 0:31 / {root}/with\\040space rw - tmpfs tmpfs rw
"""


def _mounts_from(table, default=("tmpfs", "/")):
    """Fake mount_entry: path -> (fstype, root); unknown paths look like a tmpfs."""
    def mount_entry(path, mountinfo=None):
        return table.get(os.path.normpath(path), default)
    return mount_entry


class CgroupLayoutTests(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = self._tmp.name
        self.addCleanup(self._tmp.cleanup)
        patches = [
            mock.patch.object(za, "CGROOT", self.root),
            # never touch the real mount table from a unit test
            mock.patch.object(za, "mount", lambda *a, **k: None),
            mock.patch.object(za, "log", lambda msg: None),
        ]
        for p in patches:
            p.start()
            self.addCleanup(p.stop)

    def _controllers(self, *names):
        for n in names:
            os.makedirs(os.path.join(self.root, n), exist_ok=True)

    def _v1_table(self, mount_root=CID):
        return {
            os.path.join(self.root, "memory"): ("cgroup", mount_root),
            os.path.join(self.root, "pids"): ("cgroup", mount_root),
            os.path.join(self.root, "cpu,cpuacct"): ("cgroup", mount_root),
        }

    # ── mount table parsing ────────────────────────────────────────────────────
    def test_mount_entry_reads_type_and_root_from_the_mount_table(self):
        path = os.path.join(self.root, "mountinfo")
        with open(path, "w") as f:
            f.write(FLY_POOL_MOUNTINFO.format(root=self.root))
        self.assertEqual(za.mount_entry(self.root, path), ("tmpfs", "/"))
        self.assertEqual(za.mount_entry(os.path.join(self.root, "memory"), path), ("cgroup", CID))
        self.assertEqual(za.mount_entry(os.path.join(self.root, "cpu,cpuacct"), path), ("cgroup", CID))
        self.assertEqual(za.mount_entry(os.path.join(self.root, "with space"), path), ("tmpfs", "/"))
        self.assertEqual(za.mount_entry(os.path.join(self.root, "docker/abc/zygote"), path), (None, None))
        self.assertEqual(za.mount_entry(self.root, os.path.join(self.root, "missing")), (None, None))
        self.assertEqual(za.fs_type(os.path.join(self.root, "pids"), path), "cgroup")

    # ── the production bug ─────────────────────────────────────────────────────
    def test_tmpfs_under_sys_fs_cgroup_yields_no_layout_and_creates_nothing(self):
        self._controllers("memory", "pids", "cpu,cpuacct")
        with mock.patch.object(za, "mount_entry", _mounts_from({})), \
                mock.patch.object(za, "read_proc_cgroup", lambda: FLY_HYBRID_PROCS):
            self.assertIsNone(za.setup_cgroup_base())
        for sub in ("docker", "memory/docker", "memory/zygote", "pids/zygote"):
            self.assertFalse(os.path.exists(os.path.join(self.root, sub)), sub)

    # ── cgroup v1 (Fly) ────────────────────────────────────────────────────────
    def _v1_layout(self, mount_root=CID):
        self._controllers("memory", "pids", "cpu,cpuacct")
        with mock.patch.object(za, "mount_entry", _mounts_from(self._v1_table(mount_root))), \
                mock.patch.object(za, "read_proc_cgroup", lambda: FLY_HYBRID_PROCS):
            return za.setup_cgroup_base()

    def test_v1_bases_are_relative_to_the_mount_root(self):
        # Docker (cgroupns=host, v1): the mount root IS the container's cgroup, so
        # the base is <mount>/zygote — not <mount>/docker/<id>/zygote (which would
        # nest a second /docker/<id>/ under the first; seen on a Fly Machine).
        layout = self._v1_layout(mount_root=CID)
        self.assertEqual(layout["mode"], "v1")
        self.assertEqual(layout["bases"], {
            "memory": os.path.join(self.root, "memory/zygote"),
            "pids": os.path.join(self.root, "pids/zygote"),
            "cpuacct": os.path.join(self.root, "cpu,cpuacct/zygote"),
        })
        for b in layout["bases"].values():
            self.assertTrue(os.path.isdir(b))

    def test_v1_bases_include_the_cgroup_path_when_the_mount_root_is_slash(self):
        layout = self._v1_layout(mount_root="/")
        self.assertEqual(layout["bases"]["memory"], os.path.join(self.root, "memory/docker/abc/zygote"))
        self.assertEqual(layout["bases"]["pids"], os.path.join(self.root, "pids/docker/abc/zygote"))

    def test_v1_leaf_writes_limits_and_places_pid_in_every_controller(self):
        layout = self._v1_layout()
        with mock.patch.object(za, "CG_LAYOUT", layout):
            leaf = za.make_cgroup_leaf(4242, 7, 128 * 1024 * 1024, 64)
        self.assertEqual(leaf["mode"], "v1")
        mem = leaf["paths"]["memory"]
        self.assertEqual(mem, os.path.join(self.root, "memory/zygote/job7"))
        with open(os.path.join(mem, "memory.limit_in_bytes")) as f:
            self.assertEqual(f.read(), str(128 * 1024 * 1024))
        with open(os.path.join(leaf["paths"]["pids"], "pids.max")) as f:
            self.assertEqual(f.read(), "64")
        for d in leaf["paths"].values():
            with open(os.path.join(d, "cgroup.procs")) as f:
                self.assertEqual(f.read(), "4242")
        self.assertEqual(za._cgroup_procs_path(leaf), os.path.join(mem, "cgroup.procs"))

    def test_v1_requires_memory_and_pids(self):
        self._controllers("memory")
        table = {os.path.join(self.root, "memory"): ("cgroup", CID)}
        procs = {8: (("memory",), CID), 0: ((), CID)}
        with mock.patch.object(za, "mount_entry", _mounts_from(table)), \
                mock.patch.object(za, "read_proc_cgroup", lambda: procs):
            self.assertIsNone(za.setup_cgroup_base())

    # ── cgroup v2 ──────────────────────────────────────────────────────────────
    def test_v2_root_is_detected_from_the_mount_type(self):
        table = {self.root: ("cgroup2", "/")}
        procs = {0: ((), CID)}
        with mock.patch.object(za, "mount_entry", _mounts_from(table)), \
                mock.patch.object(za, "read_proc_cgroup", lambda: procs):
            layout = za.setup_cgroup_base()
        self.assertEqual(layout, {"mode": "v2", "base": os.path.join(self.root, "docker/abc/zygote")})
        with mock.patch.object(za, "CG_LAYOUT", layout):
            leaf = za.make_cgroup_leaf(99, 3, 1 << 20, 16)
        unified = leaf["paths"]["unified"]
        self.assertEqual(unified, os.path.join(self.root, "docker/abc/zygote/job3"))
        with open(os.path.join(unified, "memory.max")) as f:
            self.assertEqual(f.read(), str(1 << 20))
        with open(os.path.join(unified, "cgroup.procs")) as f:
            self.assertEqual(f.read(), "99")

    def test_v2_private_cgroupns_maps_to_the_mount_point(self):
        # cgroupns=private: /proc/self/cgroup says "0::/" and the mount root is the
        # container's cgroup — the base is <mount>/zygote.
        table = {self.root: ("cgroup2", CID)}
        procs = {0: ((), "/")}
        with mock.patch.object(za, "mount_entry", _mounts_from(table)), \
                mock.patch.object(za, "read_proc_cgroup", lambda: procs):
            layout = za.setup_cgroup_base()
        self.assertEqual(layout["base"], os.path.join(self.root, "zygote"))

    # ── killing ────────────────────────────────────────────────────────────────
    def test_kill_session_always_signals_the_pidns_init(self):
        with mock.patch.object(za.os, "kill") as kill:
            za.kill_session(None, 4242)
        kill.assert_called_once_with(4242, za.SIGKILL)

    def test_kill_session_v1_also_kills_every_pid_listed_in_the_leaf(self):
        layout = self._v1_layout()
        with mock.patch.object(za, "CG_LAYOUT", layout):
            leaf = za.make_cgroup_leaf(4242, 1, 1 << 20, 16)
        with open(os.path.join(leaf["paths"]["memory"], "cgroup.procs"), "w") as f:
            f.write("501\n502\n")
        with mock.patch.object(za.os, "kill") as kill:
            za.kill_session(leaf, 4242)
        self.assertEqual(
            sorted(c.args for c in kill.call_args_list),
            [(501, za.SIGKILL), (502, za.SIGKILL), (4242, za.SIGKILL)],
        )

    # ── parsing ────────────────────────────────────────────────────────────────
    def test_read_proc_cgroup_parses_a_hybrid_file(self):
        content = (
            "10:cpuset:/docker/abc\n"
            "5:cpu,cpuacct:/docker/abc\n"
            "3:pids:/docker/abc\n"
            "0::/docker/abc\n"
        )
        path = os.path.join(self.root, "cgroup")
        with open(path, "w") as f:
            f.write(content)
        self.assertEqual(za.read_proc_cgroup(path), {
            10: (("cpuset",), CID),
            5: (("cpu", "cpuacct"), CID),
            3: (("pids",), CID),
            0: ((), CID),
        })


if __name__ == "__main__":
    unittest.main()
