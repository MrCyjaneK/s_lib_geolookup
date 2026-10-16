#!/bin/bash
set -euo pipefail
PBF=${1:?usage: geolookup-extract <planet.pbf> <out-dir>}
OUT=${2:?usage: geolookup-extract <planet.pbf> <out-dir>}
exec geolookup-build -pbf "$PBF" -out "$OUT" -simplify-m 250 -jobs "$(nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 4)"
