package geolookup

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type PackOptions struct {
	Scale   int32  // 0 → DefaultScale
	MinArea uint64 // 0 still quantizes; VW skipped
	Jobs    int    // 0 → GOMAXPROCS; parallel JSON parse + VW
}

func (o PackOptions) scale() int32 {
	if o.Scale == 0 {
		return DefaultScale
	}
	return o.Scale
}

func (o PackOptions) jobs() int {
	if o.Jobs > 0 {
		return o.Jobs
	}
	n := runtime.GOMAXPROCS(0)
	if n < 1 {
		return 1
	}
	return n
}

func packLog(format string, args ...any) {
	fmt.Fprintf(os.Stderr, time.Now().Format("15:04:05")+" "+format+"\n", args...)
}

func Pack(adminPath, placesPath, outDir string, opts PackOptions) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	jobs := opts.jobs()
	packLog("pack jobs=%d", jobs)
	if adminPath != "" {
		splitDir, err := os.MkdirTemp(outDir, ".split-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(splitDir)
		packLog("splitting %s by admin_level…", adminPath)
		paths, err := splitAdminByLevel(adminPath, splitDir, jobs)
		if err != nil {
			return err
		}
		levels := make([]int, 0, len(paths))
		for lv := range paths {
			levels = append(levels, lv)
		}
		sort.Ints(levels)
		packLog("packing %d admin levels (one at a time for RAM)", len(levels))
		for i, lv := range levels {
			packLog("[%d/%d] admin_level=%d: reading %s", i+1, len(levels), lv, paths[lv])
			polys, err := readAdmin(paths[lv], float64(opts.scale()), jobs)
			if err != nil {
				return err
			}
			packLog("[%d/%d] admin_level=%d: %d polygons → simplify+write", i+1, len(levels), lv, len(polys))
			if err := PackFeatures(polys, nil, outDir, opts); err != nil {
				return err
			}
			packLog("[%d/%d] admin_level=%d: done → admin_%d.glp", i+1, len(levels), lv, lv)
		}
	}
	if placesPath == "" {
		return nil
	}
	packLog("reading places %s", placesPath)
	places, err := readPlaces(placesPath, jobs)
	if err != nil {
		return err
	}
	packLog("packing %d places → places.kdt", len(places))
	return PackFeatures(nil, places, outDir, opts)
}

func PackFeatures(polys []packedPoly, places []packedPlace, outDir string, opts PackOptions) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	scale := opts.scale()
	jobs := opts.jobs()
	byLevel := map[int][]packedPoly{}
	for _, p := range polys {
		if p.level <= 0 || len(p.rings) == 0 {
			continue
		}
		ok := false
		for _, r := range p.rings {
			if !r.hole && len(r.pts) >= 4 {
				ok = true
				break
			}
		}
		if !ok {
			continue
		}
		byLevel[p.level] = append(byLevel[p.level], p)
	}
	for level, feats := range byLevel {
		if opts.MinArea > 0 {
			packLog("  admin_level=%d: simplifying %d polygons (jobs=%d)…", level, len(feats), jobs)
			simplifyTopology(feats, opts.MinArea, jobs)
		}
		path := filepath.Join(outDir, fmt.Sprintf("admin_%d.glp", level))
		packLog("  admin_level=%d: writing %s", level, filepath.Base(path))
		if err := writePolyFile(path, level, scale, feats); err != nil {
			return err
		}
	}
	if len(places) > 0 {
		if err := writeKdFile(filepath.Join(outDir, "places.kdt"), scale, places); err != nil {
			return err
		}
	}
	return nil
}

func readAdmin(path string, scale float64, jobs int) ([]packedPoly, error) {
	var (
		mu  sync.Mutex
		out []packedPoly
	)
	err := readFeatures(path, jobs, func(f geoFeature) error {
		level := propInt(f.Properties, "admin_level")
		if level <= 0 {
			return nil
		}
		rings, err := geomRings(f.Geometry, scale)
		if err != nil || len(rings) == 0 {
			return err
		}
		p := packedPoly{
			osmID:   propInt64(f.Properties, "@id", "osm_id", "id"),
			level:   level,
			name:    propString(f.Properties, "name"),
			place:   propString(f.Properties, "place", "linked_place"),
			country: countryISO(f.Properties),
			rings:   rings,
		}
		mu.Lock()
		out = append(out, p)
		mu.Unlock()
		return nil
	})
	return out, err
}

func readPlaces(path string, jobs int) ([]packedPlace, error) {
	var (
		mu  sync.Mutex
		out []packedPlace
	)
	err := readFeatures(path, jobs, func(f geoFeature) error {
		kind := strings.ToLower(propString(f.Properties, "place"))
		switch kind {
		case "suburb", "neighbourhood", "quarter", "locality":
		default:
			return nil
		}
		lon, lat, ok := geomPoint(f.Geometry)
		if !ok {
			return nil
		}
		p := packedPlace{
			osmID: propInt64(f.Properties, "@id", "osm_id", "id"),
			name:  propString(f.Properties, "name"),
			kind:  kind,
			lon:   lon,
			lat:   lat,
		}
		mu.Lock()
		out = append(out, p)
		mu.Unlock()
		return nil
	})
	return out, err
}

type geoFeature struct {
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
	Geometry   geoGeom        `json:"geometry"`
}

type geoGeom struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates"`
}

func splitAdminByLevel(path, dir string, jobs int) (map[int]string, error) {
	var (
		mu    sync.Mutex
		files = map[int]*os.File{}
		paths = map[int]string{}
		n     atomic.Uint64
	)
	err := readFeatureLines(path, jobs, func(line []byte, feat geoFeature) error {
		level := propInt(feat.Properties, "admin_level")
		if level <= 0 {
			return nil
		}
		mu.Lock()
		f := files[level]
		if f == nil {
			p := filepath.Join(dir, fmt.Sprintf("admin_%d.geojsons", level))
			var err error
			f, err = os.Create(p)
			if err != nil {
				mu.Unlock()
				return err
			}
			files[level] = f
			paths[level] = p
		}
		if _, err := f.Write(line); err != nil {
			mu.Unlock()
			return err
		}
		if len(line) == 0 || line[len(line)-1] != '\n' {
			if _, err := f.Write([]byte{'\n'}); err != nil {
				mu.Unlock()
				return err
			}
		}
		nLevels := len(paths)
		mu.Unlock()
		if c := n.Add(1); c%100_000 == 0 {
			packLog("  split progress: %d features → %d levels", c, nLevels)
		}
		return nil
	})
	mu.Lock()
	for _, f := range files {
		_ = f.Close()
	}
	nLevels := len(paths)
	mu.Unlock()
	if err == nil {
		packLog("  split done: %d features → %d levels", n.Load(), nLevels)
	}
	return paths, err
}

func readFeatures(path string, jobs int, fn func(geoFeature) error) error {
	return readFeatureLines(path, jobs, func(_ []byte, feat geoFeature) error {
		return fn(feat)
	})
}

func trimGeoJSONSeqLine(line []byte) []byte {
	line = bytes.TrimSpace(line)
	line = bytes.TrimPrefix(line, []byte{0x1e})
	return bytes.TrimSpace(line)
}

func readFeatureLines(path string, jobs int, fn func(line []byte, feat geoFeature) error) error {
	if jobs <= 0 {
		jobs = runtime.GOMAXPROCS(0)
	}
	if jobs < 1 {
		jobs = 1
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 4096)
	n, _ := f.Read(head)
	head = head[:n]
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	trimmed := trimGeoJSONSeqLine(head)
	if bytes.Contains(trimmed, []byte(`"FeatureCollection"`)) {
		var fc struct {
			Features []geoFeature `json:"features"`
		}
		if err := json.NewDecoder(f).Decode(&fc); err != nil {
			return err
		}
		for _, feat := range fc.Features {
			raw, err := json.Marshal(feat)
			if err != nil {
				return err
			}
			if err := fn(raw, feat); err != nil {
				return err
			}
		}
		return nil
	}

	type item struct {
		line []byte
	}
	ch := make(chan item, jobs*4)
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	var once sync.Once
	fail := func(e error) {
		once.Do(func() { errCh <- e })
	}
	for w := 0; w < jobs; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range ch {
				var feat geoFeature
				if err := json.Unmarshal(it.line, &feat); err != nil {
					fail(err)
					continue
				}
				if err := fn(it.line, feat); err != nil {
					fail(err)
				}
			}
		}()
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 512*1024*1024)
	var scanned atomic.Uint64
	lastLog := time.Now()
	for sc.Scan() {
		select {
		case err := <-errCh:
			close(ch)
			wg.Wait()
			return err
		default:
		}
		line := trimGeoJSONSeqLine(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		cp := append([]byte(nil), line...)
		select {
		case ch <- item{cp}:
		case err := <-errCh:
			close(ch)
			wg.Wait()
			return err
		}
		c := scanned.Add(1)
		if time.Since(lastLog) >= 5*time.Second {
			packLog("  reading %s: %d features…", filepath.Base(path), c)
			lastLog = time.Now()
		}
	}
	close(ch)
	wg.Wait()
	select {
	case err := <-errCh:
		return err
	default:
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return nil
}

func geomRings(g geoGeom, scale float64) ([]ring, error) {
	switch g.Type {
	case "Polygon":
		var coords [][][]float64
		if err := json.Unmarshal(g.Coordinates, &coords); err != nil {
			return nil, err
		}
		return polygonRings(coords, scale), nil
	case "MultiPolygon":
		var coords [][][][]float64
		if err := json.Unmarshal(g.Coordinates, &coords); err != nil {
			return nil, err
		}
		var rings []ring
		for _, poly := range coords {
			rings = append(rings, polygonRings(poly, scale)...)
		}
		return rings, nil
	default:
		return nil, nil
	}
}

func polygonRings(coords [][][]float64, scale float64) []ring {
	out := make([]ring, 0, len(coords))
	for i, c := range coords {
		pts := make([]qpoint, 0, len(c))
		for _, xy := range c {
			if len(xy) < 2 {
				continue
			}
			pts = append(pts, quantize(xy[0], xy[1], scale))
		}
		pts = closeRing(pts)
		if len(pts) < 4 {
			continue
		}
		out = append(out, ring{pts: pts, hole: i > 0})
	}
	return out
}

func geomPoint(g geoGeom) (lon, lat float64, ok bool) {
	if g.Type != "Point" {
		return 0, 0, false
	}
	var xy []float64
	if err := json.Unmarshal(g.Coordinates, &xy); err != nil || len(xy) < 2 {
		return 0, 0, false
	}
	return xy[0], xy[1], true
}

func propString(p map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := p[k]; ok {
			switch t := v.(type) {
			case string:
				if t != "" {
					return t
				}
			case float64:
				return strconv.FormatInt(int64(t), 10)
			}
		}
	}
	return ""
}

func propInt(p map[string]any, keys ...string) int {
	s := propString(p, keys...)
	if s == "" {
		return 0
	}
	if i := strings.IndexAny(s, ";/, "); i >= 0 {
		s = s[:i]
	}
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func propInt64(p map[string]any, keys ...string) int64 {
	s := propString(p, keys...)
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

func countryISO(p map[string]any) string {
	s := propString(p, "ISO3166-1:alpha2", "ISO3166-1", "iso3166_1_alpha2", "iso3166-1-alpha2", "ISO3166_1_alpha2")
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) >= 2 {
		return s[:2]
	}
	return ""
}
