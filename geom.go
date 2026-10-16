package geolookup

import "math"

const (
	// DefaultScale is ~1.1cm at the equator (degrees * 1e7 fits in int32).
	DefaultScale = 10_000_000

	// DefaultSimplifyM is the Visvalingam-Whyatt characteristic length.
	DefaultSimplifyM = 250

	// DefaultPointMaxDistanceM is the starting nearest-place cutoff.
	DefaultPointMaxDistanceM = 1500

	earthRadiusM        = 6_371_000
	equatorMetersPerDeg = 111_320.0
)

// MinAreaFromMeters is the VW 2*triangle-area threshold in quantized units for
// a metres*metres triangle at the equator.
func MinAreaFromMeters(metres float64, scale int32) uint64 {
	if metres <= 0 || scale <= 0 {
		return 0
	}
	q := metres * float64(scale) / equatorMetersPerDeg
	return uint64(q*q + 0.5)
}

var DefaultMinArea = MinAreaFromMeters(DefaultSimplifyM, DefaultScale)

type qpoint struct{ X, Y int32 }

func quantize(lon, lat, scale float64) qpoint {
	return qpoint{
		X: int32(math.Round(lon * scale)),
		Y: int32(math.Round(lat * scale)),
	}
}

func unquantize(p qpoint, scale float64) (lon, lat float64) {
	return float64(p.X) / scale, float64(p.Y) / scale
}

func shoelace2(ring []qpoint) int64 {
	if len(ring) < 3 {
		return 0
	}
	n := len(ring)
	if ring[0] == ring[n-1] {
		n--
	}
	var s int64
	j := n - 1
	for i := 0; i < n; i++ {
		s += int64(ring[j].X)*int64(ring[i].Y) - int64(ring[i].X)*int64(ring[j].Y)
		j = i
	}
	return s
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func polygonArea2(rings []ring) uint64 {
	var a uint64
	for _, r := range rings {
		v := uint64(abs64(shoelace2(r.pts)))
		if r.hole {
			if v > a {
				a = 0
			} else {
				a -= v
			}
		} else {
			a += v
		}
	}
	return a
}

func bboxOf(pts []qpoint) (minX, minY, maxX, maxY int32) {
	if len(pts) == 0 {
		return
	}
	minX, minY = pts[0].X, pts[0].Y
	maxX, maxY = minX, minY
	for _, p := range pts[1:] {
		if p.X < minX {
			minX = p.X
		}
		if p.X > maxX {
			maxX = p.X
		}
		if p.Y < minY {
			minY = p.Y
		}
		if p.Y > maxY {
			maxY = p.Y
		}
	}
	return
}

func inBBox(x, y, minX, minY, maxX, maxY int32) bool {
	return x >= minX && x <= maxX && y >= minY && y <= maxY
}

// Ray-cast; true if the point is strictly inside the ring (even-odd on this ring).
func ringContains(x, y int32, ring []qpoint) bool {
	n := len(ring)
	if n < 3 {
		return false
	}
	if ring[0] == ring[n-1] {
		n--
	}
	inside := false
	j := n - 1
	for i := 0; i < n; i++ {
		yi, yj := ring[i].Y, ring[j].Y
		if (yi > y) != (yj > y) {
			xi, xj := ring[i].X, ring[j].X
			dy := int64(yj - yi)
			lhs := int64(x-xi) * dy
			rhs := int64(xj-xi) * int64(y-yi)
			if dy < 0 {
				if lhs > rhs {
					inside = !inside
				}
			} else if lhs < rhs {
				inside = !inside
			}
		}
		j = i
	}
	return inside
}

type ring struct {
	pts  []qpoint
	hole bool
}

func ringsContain(x, y int32, rings []ring) bool {
	i := 0
	for i < len(rings) {
		for i < len(rings) && rings[i].hole {
			i++
		}
		if i >= len(rings) {
			break
		}
		outer := rings[i]
		i++
		inside := ringContains(x, y, outer.pts)
		for i < len(rings) && rings[i].hole {
			if inside && ringContains(x, y, rings[i].pts) {
				inside = false
			}
			i++
		}
		if inside {
			return true
		}
	}
	return false
}

func haversineM(lat1, lon1, lat2, lon2 float64) float64 {
	const deg = math.Pi / 180
	φ1, φ2 := lat1*deg, lat2*deg
	dφ := (lat2 - lat1) * deg
	dλ := (lon2 - lon1) * deg
	sφ := math.Sin(dφ / 2)
	sλ := math.Sin(dλ / 2)
	a := sφ*sφ + math.Cos(φ1)*math.Cos(φ2)*sλ*sλ
	return 2 * earthRadiusM * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// 16-bit Hilbert index of (x,y) in [0, n). n must be a power of two.
func hilbertIndex(n, x, y uint32) uint32 {
	var d uint32
	for s := n / 2; s > 0; s /= 2 {
		var rx, ry uint32
		if x&s != 0 {
			rx = 1
		}
		if y&s != 0 {
			ry = 1
		}
		d += s * s * ((3 * rx) ^ ry)
		x, y = hilbertRot(n, x, y, rx, ry)
	}
	return d
}

func hilbertRot(n, x, y, rx, ry uint32) (uint32, uint32) {
	if ry == 0 {
		if rx == 1 {
			x = n - 1 - x
			y = n - 1 - y
		}
		x, y = y, x
	}
	return x, y
}
