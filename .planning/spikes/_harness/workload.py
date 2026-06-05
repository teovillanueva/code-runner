#!/usr/bin/env python3
"""Faithful "memory-active" sandbox workload for the density spike.

Mirrors a live interactive code-runner session: import the baked science stack
(what drives the prod ~110 MB active footprint), allocate a small identical
buffer, print "ready", then BLOCK on stdin — holding the slot open exactly like
a real held-open session governed by the three clocks.

Two env knobs:
  CR_KSM_MERGE=1   -> call prctl(PR_SET_MEMORY_MERGE, 1) so KSM can scan this
                     process's anonymous pages (containers are NOT mergeable by
                     default; kernel >= 6.4 required for the prctl).
  CR_IDLE=1        -> skip the heavy imports; measure the bare-interpreter idle
                     footprint instead (the ~10 MB idle-session number).
"""
import os
import sys

if os.environ.get("CR_KSM_MERGE") == "1":
    try:
        import ctypes
        PR_SET_MEMORY_MERGE = 67  # linux/prctl.h, kernel >= 6.4
        libc = ctypes.CDLL(None, use_errno=True)
        rc = libc.prctl(PR_SET_MEMORY_MERGE, 1, 0, 0, 0)
        sys.stderr.write(f"prctl(PR_SET_MEMORY_MERGE)={rc} errno={ctypes.get_errno()}\n")
    except Exception as e:  # pragma: no cover
        sys.stderr.write(f"prctl failed: {e}\n")

if os.environ.get("CR_IDLE") != "1":
    # The science stack baked into languages/python-3.12 that prod sandboxes can
    # actually import. NOTE: scipy / scikit-learn / statsmodels / seaborn are
    # deliberately OMITTED — they transitively trigger `numpy.testing`, which
    # CRASHES on the prod image: its Dockerfile prunes every dir named `tests`,
    # deleting numpy/_core/tests/_natype that numpy.testing imports. A real latent
    # prod bug, recorded in the spike findings. numpy + pandas + matplotlib
    # reproduce the live active footprint faithfully on their own.
    import numpy  # noqa: F401
    import pandas  # noqa: F401
    import matplotlib
    matplotlib.use("Agg")
    from matplotlib import pyplot  # noqa: F401
    import numpy as np
    # A RANDOM ~40 MB buffer, UNIQUE per sandbox (seeded from os.urandom) — this is
    # the HONEST/conservative KSM case: user data is not mergeable, so any KSM gain
    # reflects only interpreter + library anon-page dedup, not identical buffers.
    _rng = np.random.default_rng()
    _buf = _rng.random(5_000_000)  # ~40 MB unique anonymous memory
    _buf[0] = 1.0

sys.stdout.write("ready\n")
sys.stdout.flush()

# Block forever (until killed) — this is the "live slot held open" state.
try:
    sys.stdin.read()
except Exception:
    pass
# If stdin is closed/empty, idle instead of exiting so the slot stays held.
import time
while True:
    time.sleep(3600)
