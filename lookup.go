package geolookup

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	SourcePolygon = "polygon"
	SourcePoint   = "point"
)

// Hit is one containing admin polygon or a nearby point-locality.
type Hit struct {
	AdminLevel int    // raw OSM admin_level; 0 for point-only places
	Label      string // Nominatim semantic label (city, county, …)
	Name       string
	Place      string // OSM place=* / linked_place; overrides Nominatim admin label when set
	OSMID      int64
	Source     string  // SourcePolygon or SourcePoint
	DistanceM  float64 // set for point hits
	Area       uint64  // quantized 2*area; smaller is more specific
	Country    string  // ISO 3166-1 alpha-2 when known on this feature
}

// Level is every polygon match at one admin_level (smallest area first).
type Level struct {
	AdminLevel int
	Label      string
	Hits       []Hit
}

// Result is sparse: only admin_levels that contain the point, plus an optional locality.
type Result struct {
	Levels []Level
	Place  *Hit
}

type Options struct {
	PointMaxDistanceM float64 // 0 → DefaultPointMaxDistanceM
	// Progress is called before each admin-level query and before the places
	// KD-tree search. step is 1..total; nil is fine.
	Progress func(step, total int, message string)
}

type DB struct {
	levels    []*polyFile // coarse → fine
	places    *kdFile
	pointMaxM float64
	closers   []func() error
}

func Open(dir string) (*DB, error) {
	return OpenOpts(dir, Options{})
}

func OpenOpts(dir string, opts Options) (*DB, error) {
	if err := loadAddressLevels(); err != nil {
		return nil, err
	}
	maxM := opts.PointMaxDistanceM
	if maxM <= 0 {
		maxM = DefaultPointMaxDistanceM
	}
	db := &DB{pointMaxM: maxM}
	matches, err := filepath.Glob(filepath.Join(dir, "admin_*.glp"))
	if err != nil {
		return nil, err
	}
	sort.Slice(matches, func(i, j int) bool {
		return adminLevelFromName(matches[i]) < adminLevelFromName(matches[j])
	})
	for _, p := range matches {
		pf, closer, err := openPolyFile(p)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		db.levels = append(db.levels, pf)
		db.closers = append(db.closers, closer)
	}
	placePath := filepath.Join(dir, "places.kdt")
	if _, err := os.Stat(placePath); err == nil {
		kf, closer, err := openKdFile(placePath)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", placePath, err)
		}
		db.places = kf
		db.closers = append(db.closers, closer)
	}
	return db, nil
}

func (db *DB) Close() error {
	var first error
	for i := len(db.closers) - 1; i >= 0; i-- {
		if err := db.closers[i](); err != nil && first == nil {
			first = err
		}
	}
	db.closers = nil
	return first
}

func (db *DB) Lookup(lat, lon float64) (Result, error) {
	return db.LookupOpts(lat, lon, Options{PointMaxDistanceM: db.pointMaxM})
}

func (db *DB) LookupOpts(lat, lon float64, opts Options) (Result, error) {
	maxM := opts.PointMaxDistanceM
	if maxM <= 0 {
		maxM = db.pointMaxM
	}
	report := opts.Progress
	if report == nil {
		report = func(int, int, string) {}
	}
	var out Result
	if len(db.levels) == 0 && db.places == nil {
		return out, nil
	}
	scale := float64(DefaultScale)
	if len(db.levels) > 0 {
		scale = float64(db.levels[0].scale)
	} else if db.places != nil {
		scale = db.places.scale
	}
	q := quantize(lon, lat, scale)

	total := len(db.levels)
	if db.places != nil {
		total++
	}
	step := 0

	for _, lf := range db.levels {
		step++
		report(step, total, fmt.Sprintf("querying admin level %d", lf.adminLevel))
		hits := lf.query(q.X, q.Y)
		if len(hits) == 0 {
			continue
		}
		sort.Slice(hits, func(i, j int) bool {
			if hits[i].Area != hits[j].Area {
				return hits[i].Area < hits[j].Area
			}
			return hits[i].OSMID < hits[j].OSMID
		})
		out.Levels = append(out.Levels, Level{
			AdminLevel: lf.adminLevel,
			Hits:       hits,
		})
	}

	country := countryOf(out.Levels)
	for i := range out.Levels {
		lv := &out.Levels[i]
		for j := range lv.Hits {
			h := &lv.Hits[j]
			h.Label = adminLabelFor(country, h.AdminLevel, h.Place)
			if h.Country == "" {
				h.Country = country
			}
		}
		if len(lv.Hits) > 0 {
			lv.Label = lv.Hits[0].Label
		}
	}

	if db.places != nil {
		step++
		report(step, total, "finding nearby place")
		if h, ok := db.places.nearest(lat, lon, q, maxM); ok {
			h.Label = placeLabelFor(country, h.Label)
			h.Country = country
			out.Place = &h
		}
	}
	return out, nil
}

func countryOf(levels []Level) string {
	for _, lv := range levels {
		if lv.AdminLevel != 2 {
			continue
		}
		for _, h := range lv.Hits {
			if h.Country != "" {
				return h.Country
			}
		}
	}
	for _, lv := range levels {
		for _, h := range lv.Hits {
			if h.Country != "" {
				return h.Country
			}
		}
	}
	return ""
}

func adminLevelFromName(path string) int {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, ".glp")
	base = strings.TrimPrefix(base, "admin_")
	n, _ := strconv.Atoi(base)
	return n
}
