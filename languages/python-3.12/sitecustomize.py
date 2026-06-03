# code-runner: auto-capture matplotlib figures as /workspace artifacts.
#
# Python imports a module named ``sitecustomize`` automatically at interpreter
# startup (no env var, no -X flag). This file lives in site-packages so it is
# always on that import path.
#
# Goal: when untrusted exam code creates matplotlib figures WITHOUT calling
# ``savefig()``, write each open figure to ``/workspace/figure_{NNN}.png`` at
# interpreter shutdown so the worker's workspace artifact capture picks them up.
# A run that never imports matplotlib must emit ZERO figures (no blank PNGs).
#
# Why the naive ``atexit`` saver does NOT work (fact C, verified during planning):
#   matplotlib registers ``atexit.register(Gcf.destroy_all)`` when
#   ``matplotlib._pylab_helpers`` is imported. ``atexit`` runs callbacks LIFO.
#   Because sitecustomize runs FIRST (at startup), a saver we register here would
#   run LAST — i.e. AFTER ``Gcf.destroy_all`` has already wiped every figure, so
#   ``plt.get_fignums()`` returns ``[]`` and nothing is saved.
#
# The working fix: defer registration until matplotlib is actually imported, then
#   (1) UNREGISTER matplotlib's ``Gcf.destroy_all`` atexit handler, and
#   (2) register OUR saver, which saves all figures and THEN calls
#       ``Gcf.destroy_all`` itself.
# We detect the first matplotlib import by wrapping ``builtins.__import__``.

import builtins

_real_import = builtins.__import__
_installed = False


def _install():
    """Idempotently swap matplotlib's figure-destroying atexit handler for ours.

    Safe to call repeatedly; only the first effective call wires things up. If
    ``matplotlib.pyplot`` has not been imported yet (e.g. only ``matplotlib``
    was imported) this is a no-op and a later import will retry.
    """
    global _installed
    if _installed:
        return

    import sys

    plt = sys.modules.get("matplotlib.pyplot")
    if plt is None:
        # pyplot not imported yet — defer; a subsequent matplotlib import retries.
        return

    try:
        from matplotlib._pylab_helpers import Gcf
    except Exception:
        return

    import atexit

    def _saver():
        # Save every currently-open figure to /workspace, then destroy them all.
        try:
            for num in plt.get_fignums():
                try:
                    fig = plt.figure(num)
                    fig.savefig(
                        "/workspace/figure_%03d.png" % num,
                        dpi=100,
                        bbox_inches="tight",
                    )
                except Exception:
                    # A single bad figure must never crash interpreter shutdown.
                    pass
        finally:
            try:
                Gcf.destroy_all()
            except Exception:
                pass

    # Stop matplotlib from wiping figures before our saver runs, then register
    # our saver. atexit is LIFO, but destroy_all is now unregistered so ordering
    # relative to it no longer matters.
    try:
        atexit.unregister(Gcf.destroy_all)
    except Exception:
        pass
    atexit.register(_saver)

    _installed = True


def _hooked_import(name, *args, **kwargs):
    module = _real_import(name, *args, **kwargs)
    # On the first matplotlib-related import, try to install. We keep trying
    # (cheap: _install short-circuits once done) until pyplot is present.
    if not _installed and isinstance(name, str) and name.startswith("matplotlib"):
        try:
            _install()
        except Exception:
            # Capture is best-effort; never break the user's import.
            pass
    return module


builtins.__import__ = _hooked_import
