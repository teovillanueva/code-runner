#!/bin/sh
# Build the python + R sandbox images on the dind box, from the REAL repo
# Dockerfiles (mirrors prod). Classic builder (BuildKit was flaky on dind, see
# CONVENTIONS). Writes BUILD_DONE / BUILD_FAIL to stdout (the poller greps it).
set -u
export DOCKER_BUILDKIT=0
cd /work || { echo BUILD_FAIL; exit 1; }

echo "[build] python:3.12 (full sci stack)..."
if docker build -t spike/python:3.12 /work/python-3.12 >/work/build-py.log 2>&1; then
  echo "[build] python OK"
else
  echo "[build] python FAILED"; tail -20 /work/build-py.log; echo BUILD_FAIL; exit 1
fi

echo "[build] r:4.4 (jsonlite/data.table/lpSolve/ggplot2)..."
if docker build -t spike/r:4.4 /work/r-4.4 >/work/build-r.log 2>&1; then
  echo "[build] R OK"
else
  echo "[build] R FAILED"; tail -20 /work/build-r.log; echo BUILD_FAIL; exit 1
fi

echo "[build] python smoke:"
docker run --rm spike/python:3.12 python -c "import numpy,scipy,sklearn,pandas,matplotlib; print('py-import-ok')" 2>&1 | tail -2
echo "[build] R smoke:"
docker run --rm spike/r:4.4 Rscript -e "library(ggplot2);cat('r-import-ok\n')" 2>&1 | tail -2
echo BUILD_DONE
