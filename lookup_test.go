package geolookup

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func rect(level int, name, cc string, osm int64, west, south, east, north float64) packedPoly {
	scale := float64(DefaultScale)
	pts := []qpoint{
		quantize(west, south, scale),
		quantize(east, south, scale),
		quantize(east, north, scale),
		quantize(west, north, scale),
		quantize(west, south, scale),
	}
	return packedPoly{
		osmID: osm, level: level, name: name, country: cc,
		rings: []ring{{pts: pts}},
	}
}

func TestJapanHierarchyLookup(t *testing.T) {
	dir := t.TempDir()
	// Nested boxes around Tokyo Station (35.6812, 139.7671)
	polys := []packedPoly{
		rect(2, "Japan", "jp", 1, 122, 24, 146, 46),
		rect(4, "Tokyo", "jp", 2, 139.0, 35.4, 140.0, 35.9),
		rect(7, "Chiyoda", "jp", 3, 139.73, 35.67, 139.78, 35.70),
		rect(9, "Marunouchi", "jp", 4, 139.76, 35.678, 139.77, 35.685),
	}
	places := []packedPlace{{osmID: 9, name: "Otemachi", kind: "neighbourhood", lon: 139.767, lat: 35.686}}
	if err := PackFeatures(polys, places, dir, PackOptions{MinArea: 0}); err != nil {
		t.Fatal(err)
	}
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var msgs []string
	res, err := db.LookupOpts(35.6812, 139.7671, Options{
		Progress: func(step, total int, message string) {
			msgs = append(msgs, message)
			if step < 1 || step > total || total != 5 {
				t.Errorf("step=%d total=%d", step, total)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantMsgs := []string{
		"querying admin level 2",
		"querying admin level 4",
		"querying admin level 7",
		"querying admin level 9",
		"finding nearby place",
	}
	if len(msgs) != len(wantMsgs) {
		t.Fatalf("progress msgs=%v want %v", msgs, wantMsgs)
	}
	for i := range wantMsgs {
		if msgs[i] != wantMsgs[i] {
			t.Fatalf("progress msgs=%v want %v", msgs, wantMsgs)
		}
	}
	if len(res.Levels) != 4 {
		t.Fatalf("levels %d %#v", len(res.Levels), res.Levels)
	}
	want := []struct {
		level       int
		label, name string
	}{
		{2, "country", "Japan"},
		{4, "state", "Tokyo"},
		{7, "city", "Chiyoda"},
		{9, "suburb", "Marunouchi"},
	}
	for i, w := range want {
		lv := res.Levels[i]
		if lv.AdminLevel != w.level || lv.Label != w.label || lv.Hits[0].Name != w.name {
			t.Errorf("level[%d]=%d %q %q want %d %q %q", i, lv.AdminLevel, lv.Label, lv.Hits[0].Name, w.level, w.label, w.name)
		}
		if lv.Hits[0].Source != SourcePolygon {
			t.Errorf("source %s", lv.Hits[0].Source)
		}
	}
	if res.Place == nil || res.Place.Name != "Otemachi" || res.Place.Source != SourcePoint {
		t.Fatalf("place %+v", res.Place)
	}
	if res.Place.DistanceM <= 0 || res.Place.DistanceM > DefaultPointMaxDistanceM {
		t.Fatalf("distance %f", res.Place.DistanceM)
	}
}

func TestGermanyHierarchyLookup(t *testing.T) {
	dir := t.TempDir()
	polys := []packedPoly{
		rect(2, "Germany", "de", 1, 5, 47, 16, 55),
		rect(4, "Berlin", "de", 2, 13.0, 52.3, 13.8, 52.7),
		rect(6, "Mitte", "de", 3, 13.35, 52.50, 13.45, 52.54),
		rect(8, "Mitte", "de", 4, 13.38, 52.51, 13.42, 52.53),
		rect(10, "Hackescher Markt", "de", 5, 13.400, 52.521, 13.405, 52.524),
	}
	if err := PackFeatures(polys, nil, dir, PackOptions{MinArea: 0}); err != nil {
		t.Fatal(err)
	}
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	res, err := db.Lookup(52.5225, 13.4025)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]string{2: "country", 4: "state", 6: "county", 8: "city", 10: "suburb"}
	if len(res.Levels) != len(want) {
		t.Fatalf("got %d levels", len(res.Levels))
	}
	for _, lv := range res.Levels {
		if want[lv.AdminLevel] != lv.Label {
			t.Errorf("admin %d label %q want %q", lv.AdminLevel, lv.Label, want[lv.AdminLevel])
		}
	}
}

func TestSameLevelSmallestArea(t *testing.T) {
	dir := t.TempDir()
	polys := []packedPoly{
		rect(8, "big", "de", 1, 0, 0, 10, 10),
		rect(8, "small", "de", 2, 4, 4, 6, 6),
	}
	if err := PackFeatures(polys, nil, dir, PackOptions{}); err != nil {
		t.Fatal(err)
	}
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	res, err := db.Lookup(5, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Levels) != 1 || len(res.Levels[0].Hits) != 2 {
		t.Fatalf("%+v", res)
	}
	if res.Levels[0].Hits[0].Name != "small" {
		t.Fatalf("want small first, got %q", res.Levels[0].Hits[0].Name)
	}
}

func TestPointBeyondThresholdDropped(t *testing.T) {
	dir := t.TempDir()
	places := []packedPlace{{osmID: 1, name: "far", kind: "locality", lon: 10, lat: 10}}
	if err := PackFeatures(nil, places, dir, PackOptions{}); err != nil {
		t.Fatal(err)
	}
	db, err := OpenOpts(dir, Options{PointMaxDistanceM: 500})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	res, err := db.Lookup(10.1, 10.1) // ~15km
	if err != nil {
		t.Fatal(err)
	}
	if res.Place != nil {
		t.Fatalf("expected nil place, got %+v", res.Place)
	}
}

func TestGeoJSONRoundTrip(t *testing.T) {
	dir := t.TempDir()
	admin := filepath.Join(dir, "admin.json")
	js := `{
	  "type":"FeatureCollection",
	  "features":[{
	    "type":"Feature",
	    "properties":{"admin_level":"4","name":"X","osm_id":42,"ISO3166-1:alpha2":"DE"},
	    "geometry":{"type":"Polygon","coordinates":[[[0,0],[2,0],[2,2],[0,2],[0,0]]]}
	  }]
	}`
	if err := os.WriteFile(admin, []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	if err := Pack(admin, "", out, PackOptions{MinArea: 0}); err != nil {
		t.Fatal(err)
	}
	db, err := Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	res, err := db.Lookup(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Levels) != 1 || res.Levels[0].Hits[0].Name != "X" || res.Levels[0].Hits[0].OSMID != 42 {
		t.Fatalf("%+v", res)
	}
}

func TestHaversineKnownDistance(t *testing.T) {
	d := haversineM(0, 0, 1, 0)
	if math.Abs(d-111_195) > 200 {
		t.Fatalf("got %f", d)
	}
}

func TestMinAreaFromMeters250(t *testing.T) {
	a := MinAreaFromMeters(250, DefaultScale)
	if a < 400_000_000 || a > 600_000_000 {
		t.Fatalf("250m area %d", a)
	}
	if DefaultMinArea != a {
		t.Fatalf("DefaultMinArea %d want %d", DefaultMinArea, a)
	}
	if MinAreaFromMeters(0, DefaultScale) != 0 {
		t.Fatal("zero metres")
	}
}

func TestGeoJSONSeqRecordSeparator(t *testing.T) {
	dir := t.TempDir()
	admin := filepath.Join(dir, "admin.geojsons")
	line := "\x1e{\"type\":\"Feature\",\"properties\":{\"admin_level\":\"4\",\"name\":\"Y\",\"@id\":7},\"geometry\":{\"type\":\"Polygon\",\"coordinates\":[[[0,0],[1,0],[1,1],[0,1],[0,0]]]}}\n"
	if err := os.WriteFile(admin, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	if err := Pack(admin, "", out, PackOptions{MinArea: 0}); err != nil {
		t.Fatal(err)
	}
	db, err := Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	res, err := db.Lookup(0.5, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Levels) != 1 || res.Levels[0].Hits[0].Name != "Y" || res.Levels[0].Hits[0].OSMID != 7 {
		t.Fatalf("%+v", res)
	}
}
