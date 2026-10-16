package geolookup

import (
	"testing"
)

func TestSimplifyKeepsSharedEdge(t *testing.T) {
	// Two unit squares sharing x=1. Extra vertices on the shared edge.
	left := packedPoly{
		osmID: 1, level: 8, name: "L",
		rings: []ring{{pts: []qpoint{
			{0, 0}, {1, 0}, {1, 1}, {1, 2}, {0, 2}, {0, 0},
		}}},
	}
	right := packedPoly{
		osmID: 2, level: 8, name: "R",
		rings: []ring{{pts: []qpoint{
			{1, 0}, {2, 0}, {2, 2}, {1, 2}, {1, 1}, {1, 0},
		}}},
	}
	polys := []packedPoly{left, right}
	simplifyTopology(polys, 10_000_000, 1) // drop the colinear (1,1)

	shared := func(p packedPoly) (qpoint, qpoint, bool) {
		pts := p.rings[0].pts
		n := len(pts) - 1
		for i := 0; i < n; i++ {
			a, b := pts[i], pts[(i+1)%n]
			if a.X == 1 && b.X == 1 {
				return a, b, true
			}
		}
		return qpoint{}, qpoint{}, false
	}
	a1, b1, ok1 := shared(polys[0])
	a2, b2, ok2 := shared(polys[1])
	if !ok1 || !ok2 {
		t.Fatalf("missing shared edge L=%v R=%v", polys[0].rings[0].pts, polys[1].rings[0].pts)
	}
	// Same undirected edge after simplify (no gap).
	u := func(a, b qpoint) undirected { return undir(a, b) }
	if u(a1, b1) != u(a2, b2) {
		t.Fatalf("gap/overlap: L %v-%v R %v-%v", a1, b1, a2, b2)
	}
}

func TestVisvalingamOpenPolyline(t *testing.T) {
	pts := []qpoint{{0, 0}, {1, 0}, {2, 0}, {3, 0}}
	got := visvalingam(pts, 1)
	if len(got) != 2 || got[0] != pts[0] || got[len(got)-1] != pts[len(pts)-1] {
		t.Fatalf("%v", got)
	}
}
