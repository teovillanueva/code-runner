#!/bin/sh
# Tests for ensure-images.sh against a fake `docker` (no daemon needed).
# Run: sh deploy/fly/worker/ensure-images_test.sh
set -u

HERE="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$HERE/ensure-images.sh"
REG="ghcr.io/teovillanueva"
failures=0

# The fake docker keeps its state in $STATE/images (one ref per line), appends
# every call to $STATE/calls, pulls only refs listed in $STATE/pullable, and
# drains stdin on every call — so a call that is not redirected from /dev/null
# would eat the lock file and the test would notice the missing images.
setup() {
  STATE="$(mktemp -d)"
  mkdir -p "$STATE/bin"
  : >"$STATE/images"
  : >"$STATE/calls"
  : >"$STATE/pullable"
  cat >"$STATE/bin/docker" <<'FAKE'
#!/bin/sh
S="$FAKE_DOCKER_STATE"
echo "$*" >>"$S/calls"
case "$1" in login) cat >/dev/null; exit 0 ;; esac
cat >/dev/null
has() { grep -qxF "$1" "$S/images"; }
add() { has "$1" || echo "$1" >>"$S/images"; }
case "$1 $2" in
  "image inspect") has "$3" ;;
  "image ls")
    prefix=""
    for a in "$@"; do case "$a" in reference=*) prefix="${a#reference=}"; prefix="${prefix%\*}" ;; esac; done
    grep -F "$prefix" "$S/images" || true ;;
  "image rm") grep -vxF "$3" "$S/images" >"$S/images.new"; mv "$S/images.new" "$S/images" ;;
  "image prune") exit 0 ;;
  *)
    case "$1" in
      tag) has "$2" && add "$3" ;;
      pull) ref="$3"; grep -qxF "$ref" "$S/pullable" && add "$ref" ;;
      *) echo "fake docker: unexpected: $*" >&2; exit 2 ;;
    esac ;;
esac
FAKE
  chmod +x "$STATE/bin/docker"
  cat >"$STATE/lock" <<LOCK
# comment
executor/python:3.12  executor-python:3.12-new
executor/c:14         executor-c:14-new

executor/cpp:14       executor-cpp:14-new
LOCK
}

run() {
  OUT="$(PATH="$STATE/bin:$PATH" FAKE_DOCKER_STATE="$STATE" IMAGES_LOCK="$STATE/lock" \
    SANDBOX_IMAGE_REGISTRY="$REG" PULL_ATTEMPTS=2 PULL_BACKOFF_S=0 sh "$SCRIPT" 2>&1)"
  STATUS=$?
}

check() { # check <description> <command...>
  desc="$1"; shift
  if "$@"; then echo "ok   - $desc"; else echo "FAIL - $desc"; failures=$((failures + 1)); fi
}
has_image() { grep -qxF "$1" "$STATE/images"; }
lacks_image() { ! grep -qxF "$1" "$STATE/images"; }
pulls_of() { grep -c "^pull -q $1\$" "$STATE/calls"; }
out_has() { echo "$OUT" | grep -qF "$1"; }

# 1. Volume from an older golden snapshot: floating tags, no C/C++.
setup
printf '%s\n' "executor/python:3.12" "$REG/executor-python:3.12" >"$STATE/images"
printf '%s\n' "$REG/executor-python:3.12-new" "$REG/executor-c:14-new" "$REG/executor-cpp:14-new" >"$STATE/pullable"
run
check "old volume: exit 0" [ "$STATUS" -eq 0 ]
check "old volume: prints the line bake waits for" out_has "language images ready"
check "old volume: pulls the missing C image" has_image "$REG/executor-c:14-new"
check "old volume: local name for C points at the pinned image" has_image "executor/c:14"
check "old volume: local name for C++ set (lines after a blank line still read)" has_image "executor/cpp:14"
check "old volume: pulls the re-pinned python image" has_image "$REG/executor-python:3.12-new"
check "old volume: removes the superseded floating tag" lacks_image "$REG/executor-python:3.12"
rm -rf "$STATE"

# 2. Warm boot: everything pinned is already there → no registry calls.
setup
printf '%s\n' "$REG/executor-python:3.12-new" "$REG/executor-c:14-new" "$REG/executor-cpp:14-new" >"$STATE/images"
run
check "warm boot: exit 0" [ "$STATUS" -eq 0 ]
check "warm boot: no pulls" [ "$(grep -c '^pull' "$STATE/calls")" -eq 0 ]
check "warm boot: local names set" has_image "executor/python:3.12"
rm -rf "$STATE"

# 3. One image cannot be pulled: the node still comes up, nothing is removed.
setup
printf '%s\n' "executor/python:3.12" "$REG/executor-python:3.12" >"$STATE/images"
printf '%s\n' "$REG/executor-python:3.12-new" "$REG/executor-cpp:14-new" >"$STATE/pullable" # C is down
run
check "pull failure: exit 1 (entrypoint carries on)" [ "$STATUS" -eq 1 ]
check "pull failure: reports INCOMPLETE, not ready" out_has "language images INCOMPLETE"
check "pull failure: no 'ready' line (bake must not snapshot)" sh -c '! echo "$1" | grep -qF "language images ready"' _ "$OUT"
check "pull failure: retried PULL_ATTEMPTS times" [ "$(pulls_of "$REG/executor-c:14-new")" -eq 2 ]
check "pull failure: the other images still pulled" has_image "$REG/executor-cpp:14-new"
check "pull failure: superseded tags kept (no cleanup after a failure)" has_image "$REG/executor-python:3.12"
check "pull failure: says the image is missing" out_has "executor/c:14 is MISSING"
rm -rf "$STATE"

# 4. Pull fails but the volume has a previous build: keep serving it.
setup
printf '%s\n' "executor/c:14" "$REG/executor-c:14" >"$STATE/images"
printf '%s\n' "$REG/executor-python:3.12-new" "$REG/executor-cpp:14-new" >"$STATE/pullable"
run
check "stale fallback: keeps the previous build" has_image "executor/c:14"
check "stale fallback: says so" out_has "keeping the executor/c:14 already on the volume"
rm -rf "$STATE"

# 5. Missing lock file.
setup
rm -f "$STATE/lock"
run
check "missing lock: exit 1" [ "$STATUS" -eq 1 ]
check "missing lock: reports INCOMPLETE" out_has "language images INCOMPLETE"
rm -rf "$STATE"

if [ "$failures" -ne 0 ]; then
  echo "$failures check(s) failed"
  exit 1
fi
echo "all checks passed"
