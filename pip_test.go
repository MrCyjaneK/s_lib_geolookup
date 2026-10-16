package geolookup

import "testing"

func sq(x0, y0, x1, y1 int32) []qpoint {
	return []qpoint{{x0, y0}, {x1, y0}, {x1, y1}, {x0, y1}, {x0, y0}}
}

func TestRingContains(t *testing.T) {
	r := sq(0, 0, 10, 10)
	if !ringContains(5, 5, r) {
		t.Fatal("center")
	}
	if ringContains(20, 5, r) {
		t.Fatal("outside")
	}
	if ringContains(-1, 5, r) {
		t.Fatal("left")
	}
}

func TestRingsContainHole(t *testing.T) {
	rings := []ring{
		{pts: sq(0, 0, 10, 10), hole: false},
		{pts: sq(3, 3, 7, 7), hole: true},
	}
	if !ringsContain(1, 1, rings) {
		t.Fatal("in outer")
	}
	if ringsContain(5, 5, rings) {
		t.Fatal("in hole")
	}
}

func TestRingsContainMultiOuter(t *testing.T) {
	rings := []ring{
		{pts: sq(0, 0, 4, 4), hole: false},
		{pts: sq(10, 10, 14, 14), hole: false},
	}
	if !ringsContain(2, 2, rings) || !ringsContain(12, 12, rings) {
		t.Fatal("outers")
	}
	if ringsContain(7, 7, rings) {
		t.Fatal("between")
	}
}
