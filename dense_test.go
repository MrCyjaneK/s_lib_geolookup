package geolookup

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDenseGermanyLikePackSizeAndLatency(t *testing.T) {
	dir := t.TempDir()
	const cells = 40
	polys := []packedPoly{
		rect(2, "Germany", "de", 1, 0, 0, 10, 10),
		rect(4, "State", "de", 2, 0, 0, 10, 10),
	}
	id := int64(10)
	for y := 0; y < cells; y++ {
		for x := 0; x < cells; x++ {
			w := float64(x) * 10 / cells
			s := float64(y) * 10 / cells
			e := float64(x+1) * 10 / cells
			n := float64(y+1) * 10 / cells
			polys = append(polys, rect(8, fmt.Sprintf("c-%d-%d", x, y), "de", id, w, s, e, n))
			id++
		}
	}
	var places []packedPlace
	for y := 0; y < cells; y++ {
		for x := 0; x < cells; x++ {
			places = append(places, packedPlace{
				osmID: id,
				name:  fmt.Sprintf("n-%d-%d", x, y),
				kind:  "neighbourhood",
				lon:   (float64(x) + 0.5) * 10 / cells,
				lat:   (float64(y) + 0.5) * 10 / cells,
			})
			id++
		}
	}
	start := time.Now()
	if err := PackFeatures(polys, places, dir, PackOptions{MinArea: DefaultMinArea}); err != nil {
		t.Fatal(err)
	}
	packDur := time.Since(start)
	var total int64
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
			t.Logf("%s %d bytes", filepath.Base(path), info.Size())
		}
		return nil
	})
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const n = 200
	start = time.Now()
	var hits int
	for i := 0; i < n; i++ {
		lat := 0.1 + float64(i%cells)*10/cells
		lon := 0.1 + float64(i%cells)*10/cells
		res, err := db.Lookup(lat, lon)
		if err != nil {
			t.Fatal(err)
		}
		hits += len(res.Levels)
		if len(res.Levels) < 3 {
			t.Fatalf("lookup %f,%f levels=%d", lat, lon, len(res.Levels))
		}
	}
	elapsed := time.Since(start)
	t.Logf("pack %s packed_bytes=%d lookups=%d avg=%s hits=%d", packDur, total, n, elapsed/n, hits)
	if total <= 0 {
		t.Fatal("empty pack")
	}
}
