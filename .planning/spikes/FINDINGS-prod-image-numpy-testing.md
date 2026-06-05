# Finding (P1): prod Python image has a broken `numpy.testing`

**Discovered during spike density measurement (2026-06-05). Unrelated to density —
this is a correctness bug in the shipping `languages/python-3.12` image.**

## Symptom

Any user code that imports **scipy, scikit-learn, statsmodels, or seaborn** (i.e.
most of the baked science stack beyond numpy/pandas/matplotlib) crashes at import:

```
ModuleNotFoundError: No module named 'numpy._core.tests'
  File ".../numpy/testing/_private/utils.py", line 31, in <module>
    from numpy._core.tests._natype import pd_NA
```

Trigger path observed: `import scipy.linalg` → `scipy._lib.array_api_compat.numpy`
does `from numpy import *` → numpy `__getattr__` lazily `import numpy.testing` →
which imports the deleted `numpy/_core/tests/_natype`.

## Root cause

`languages/python-3.12/Dockerfile` slims site-packages with:

```dockerfile
find "$sp" -depth -type d \( -name tests -o -name test -o -name __pycache__ \) -prune -exec rm -rf '{}' +
```

This deletes **every** dir named `tests` — including `numpy/_core/tests/`, which
`numpy.testing` depends on at import time. The Dockerfile comment explicitly
intends to *keep* `numpy.testing` ("testing modules … are deliberately KEPT"),
but pruning `tests/` breaks it anyway because `numpy.testing` imports from
`numpy._core.tests._natype`.

## Impact

The image's headline value is a "university-exam scientific stack
(scipy, statsmodels, scikit-learn, seaborn, …)". As shipped, importing any of
those raises `ModuleNotFoundError`. Verified by building the exact prod Dockerfile
on a clean Fly machine. Needs verification that CI's published
`ghcr.io/teovillanueva/executor/python:3.12` is built from this same Dockerfile
(it is, per `.github/workflows/release-images.yml`) → the published image is
almost certainly affected.

## Suggested fix (any one)

- Exclude numpy's needed tests dir from the prune:
  `find "$sp" -depth -type d \( -name tests -o -name test \) -not -path '*/numpy/_core/tests*' -prune -exec rm -rf '{}' +`
  (or more robustly, only prune under packages known not to import their own tests).
- Or keep `numpy/_core/tests/_natype.py` specifically.
- Add a build-time smoke test to the image: `python -c "import scipy, sklearn, statsmodels.api, seaborn"` so a broken prune fails the build.

## Next step

Route into the real backlog (this is not a spike artifact): `/gsd-capture` or a
GitHub issue. Add the import smoke-test to the language image CI so this class of
regression is caught at build.
