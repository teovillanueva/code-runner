#!/usr/bin/env python3
"""Zygote / copy-on-write density probe.

Import the heavy stack ONCE in a parent process, then fork() one child per
"session". Each child allocates its own UNIQUE ~40 MB working buffer and blocks
forever. Because of copy-on-write, every child shares the parent's ~70 MB of
imported library pages physically — they are paid ONCE, not per child.

The parent ramps children until host MemAvailable hits a safety floor, then
prints the ceiling. Compare against the container-per-sandbox ceiling (~30):
this is the "heavy sandboxes share their imports" lever.
"""
import os
import sys
import time

import numpy  # noqa: F401
import pandas  # noqa: F401
import matplotlib
matplotlib.use("Agg")
from matplotlib import pyplot  # noqa: F401
import numpy as np

SAFETY_KB = 220000
HARD_CAP = 220


def mem_avail_kb():
    with open("/proc/meminfo") as f:
        for line in f:
            if line.startswith("MemAvailable"):
                return int(line.split()[1])


n = 0
base = mem_avail_kb()
while n < HARD_CAP:
    if mem_avail_kb() < SAFETY_KB:
        break
    pid = os.fork()
    if pid == 0:
        # child: unique working set, then hold the slot open
        rng = np.random.default_rng()
        buf = rng.random(5_000_000)  # ~40 MB unique anonymous memory
        buf[0] = 1.0
        sys.stdout.write("")  # touch
        while True:
            time.sleep(3600)
    n += 1
    time.sleep(2)  # let the child's buffer fault in to steady state

after = mem_avail_kb()
used = base - after
per = used // n if n else 0
sys.stderr.write(
    f"ZYGOTE_CEILING={n} used_kb={used} marginal_per_child_kb={per} base_kb={base} after_kb={after}\n"
)
sys.stderr.flush()
# keep parent + children alive briefly so the harness can sample
time.sleep(20)
