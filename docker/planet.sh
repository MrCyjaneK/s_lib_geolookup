#!/bin/bash
# Download planet-latest.osm.pbf and pack geolookup files.
# Usage: geolookup-planet <data-dir>
#        data-dir/planet-latest.osm.pbf is downloaded (resumable)
#        data-dir/out receives admin_*.glp and places.kdt
set -euo pipefail
DEST=${1:?usage: geolookup-planet <data-dir>}
mkdir -p "$DEST/out"
PBF="$DEST/planet-latest.osm.pbf"
URL="https://planet.openstreetmap.org/pbf/planet-latest.osm.pbf"
echo "downloading $URL -> $PBF"
curl -L --retry 8 --retry-delay 5 -C - -o "$PBF" "$URL"
geolookup-extract "$PBF" "$DEST/out"
echo "packed into $DEST/out"
echo "planet PBF can be deleted to reclaim ~88GiB: rm $PBF"
