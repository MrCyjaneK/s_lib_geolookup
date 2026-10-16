# geolookup

> Fully slopped. Sorry. Will cleanup at some point

Standalone Go module: reverse-geocode a WGS84 point against OSM administrative
boundaries (every `admin_level`, not a fixed subset) plus point-only localities.

## Lookup

```go
db, err := geolookup.Open("/path/to/packed")
res, err := db.Lookup(35.6812, 139.7671)
```

`Result.Levels` is sparse: one entry per containing `admin_level`, coarse to fine.
Several polygons may match a level; they are ordered by increasing area (most
specific first). `Result.Place` is the nearest `place=suburb|neighbourhood|quarter|locality`
node within `Options.PointMaxDistanceM` (default 1500 m), tagged `source: point`
and `distance_m`.

Raw `admin_level` is always kept. Semantic labels (`city`, `county`, …) come from
Nominatim: vendored `settings/address-levels.json` (country-specific ranks) plus
`get_label_tag` / `ADMIN_LABELS` from Nominatim `classtypes.py`.

## Packed files

One `admin_{level}.glp` per admin_level (Hilbert-packed R-tree, int32 coordinates,
mmap). `places.kdt` is a static k-d tree of point localities (also mmap).

Quantization is degrees × 1e7 (int32). Topology-aware Visvalingam–Whyatt runs on
shared arcs **per admin_level** with a 250 m characteristic length (`-simplify-m 250`).

## Planet extract

Yes — production assets are built from the **full planet** PBF (~88 GiB), not a
country extract. Country/region PBFs work for testing (`-pbf` accepts any OSM
PBF); the shipped `admin_*.glp` / `places.kdt` expect worldwide coverage.

`docker/Dockerfile` installs osmium-tool and the packer:

```
cd lib/geolookup
docker build -t geolookup-build -f docker/Dockerfile .
# download planet + pack (needs ~200 GiB free at peak):
docker run --rm -v "$PWD/data:/data" --entrypoint geolookup-planet geolookup-build /data
# or pack an existing PBF:
docker run --rm -v "$PWD/data:/data" geolookup-build /data/planet-latest.osm.pbf /data/out
```

Or with osmium on PATH:

```
curl -L -C - -o planet-latest.osm.pbf https://planet.openstreetmap.org/pbf/planet-latest.osm.pbf
go run ./cmd/geolookup-build -pbf planet-latest.osm.pbf -out ./out -simplify-m 250
```

`-jobs N` sets packer workers (JSON parse + Visvalingam); default is `GOMAXPROCS`.
Admin levels are still packed one at a time so peak RAM stays roughly one level.
Osmium `tags-filter` / `export` for admin and places run in parallel.

After `tags-filter`, osmium export (disk-backed node index) writes GeoJSONSeq;
the packer then splits by `admin_level`. Need ~200 GiB free at peak (planet +
filtered PBF + GeoJSONSeq). Delete the planet PBF after extract if space is tight;
packed output is hundreds of MB, not tens of GB.

## Tests

```
cd lib/geolookup && go test ./...
```
