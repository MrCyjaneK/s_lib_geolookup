package geolookup

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"sort"
)

const (
	kdtMagic   = "KDT1"
	kdtVersion = 1
	kdtHeaderN = 64
	kdNodeSize = 32
)

type packedPlace struct {
	osmID int64
	name  string
	kind  string
	lon   float64
	lat   float64
}

type kdFile struct {
	scale float64
	n     int
	root  int32
	data  []byte
	nodes []byte
	strs  []byte
}

func openKdFile(path string) (*kdFile, func() error, error) {
	data, closer, err := mapFile(path)
	if err != nil {
		return nil, nil, err
	}
	kf, err := parseKD(data)
	if err != nil {
		_ = closer()
		return nil, nil, err
	}
	return kf, closer, nil
}

func parseKD(data []byte) (*kdFile, error) {
	if len(data) < kdtHeaderN || string(data[:4]) != kdtMagic {
		return nil, fmt.Errorf("not a KDT file")
	}
	if binary.LittleEndian.Uint32(data[4:]) != kdtVersion {
		return nil, fmt.Errorf("unsupported KDT version")
	}
	n := int(binary.LittleEndian.Uint32(data[8:]))
	scale := binary.LittleEndian.Uint32(data[12:])
	root := int32(binary.LittleEndian.Uint32(data[16:]))
	nodesOff := binary.LittleEndian.Uint32(data[20:])
	strOff := binary.LittleEndian.Uint32(data[24:])
	if int(nodesOff)+n*kdNodeSize > len(data) || int(strOff) > len(data) {
		return nil, fmt.Errorf("truncated KDT")
	}
	return &kdFile{
		scale: float64(scale),
		n:     n,
		root:  root,
		data:  data,
		nodes: data[nodesOff:strOff],
		strs:  data[strOff:],
	}, nil
}

func (f *kdFile) node(i int32) (x, y, left, right int32, osmID int64, name, kind string, axis uint8) {
	off := int(i) * kdNodeSize
	b := f.nodes[off : off+kdNodeSize]
	x = int32(binary.LittleEndian.Uint32(b[0:]))
	y = int32(binary.LittleEndian.Uint32(b[4:]))
	left = int32(binary.LittleEndian.Uint32(b[8:]))
	right = int32(binary.LittleEndian.Uint32(b[12:]))
	osmID = int64(binary.LittleEndian.Uint64(b[16:]))
	nameOff := binary.LittleEndian.Uint32(b[24:])
	nameLen := binary.LittleEndian.Uint16(b[28:])
	kindLen := b[30]
	axis = b[31]
	if int(nameOff)+int(nameLen)+int(kindLen) <= len(f.strs) {
		name = string(f.strs[nameOff : nameOff+uint32(nameLen)])
		kind = string(f.strs[nameOff+uint32(nameLen) : nameOff+uint32(nameLen)+uint32(kindLen)])
	}
	return
}

func (f *kdFile) nearest(lat, lon float64, q qpoint, maxM float64) (Hit, bool) {
	if f.n == 0 || f.root < 0 {
		return Hit{}, false
	}
	bestM := maxM
	var best Hit
	found := false
	scale := f.scale
	var walk func(idx int32)
	walk = func(idx int32) {
		if idx < 0 {
			return
		}
		x, y, left, right, osmID, name, kind, axis := f.node(idx)
		nLon, nLat := unquantize(qpoint{x, y}, scale)
		d := haversineM(lat, lon, nLat, nLon)
		if d <= bestM {
			bestM = d
			found = true
			best = Hit{
				Label:     kind,
				Name:      name,
				Place:     kind,
				OSMID:     osmID,
				Source:    SourcePoint,
				DistanceM: d,
			}
		}
		qv, nv := q.X, x
		first, second := left, right
		if axis == 1 {
			qv, nv = q.Y, y
		}
		if qv > nv {
			first, second = right, left
		}
		walk(first)
		if kdPlaneMaybeCloser(axis, qv, nv, bestM, lat, scale) {
			walk(second)
		}
	}
	walk(f.root)
	return best, found
}

func kdPlaneMaybeCloser(axis uint8, qv, nv int32, bestM, lat, scale float64) bool {
	dlat := bestM / 111_000
	cos := math.Cos(lat * math.Pi / 180)
	if cos < 0.1 {
		cos = 0.1
	}
	dlon := bestM / (111_000 * cos)
	limit := dlon
	if axis == 1 {
		limit = dlat
	}
	delta := qv - nv
	if delta < 0 {
		delta = -delta
	}
	return float64(delta) <= limit*scale+1
}

type kdBuild struct {
	x, y  int32
	osmID int64
	name  string
	kind  string
}

func writeKdFile(path string, scale int32, places []packedPlace) error {
	pts := make([]kdBuild, 0, len(places))
	for _, p := range places {
		q := quantize(p.lon, p.lat, float64(scale))
		pts = append(pts, kdBuild{q.X, q.Y, p.osmID, p.name, p.kind})
	}
	var nodes []byte
	var strs []byte
	root := buildKD(pts, 0, &nodes, &strs)
	hdr := make([]byte, kdtHeaderN)
	copy(hdr, kdtMagic)
	binary.LittleEndian.PutUint32(hdr[4:], kdtVersion)
	binary.LittleEndian.PutUint32(hdr[8:], uint32(len(pts)))
	binary.LittleEndian.PutUint32(hdr[12:], uint32(scale))
	binary.LittleEndian.PutUint32(hdr[16:], uint32(root))
	binary.LittleEndian.PutUint32(hdr[20:], kdtHeaderN)
	binary.LittleEndian.PutUint32(hdr[24:], kdtHeaderN+uint32(len(nodes)))
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(hdr); err != nil {
		return err
	}
	if _, err := f.Write(nodes); err != nil {
		return err
	}
	_, err = f.Write(strs)
	return err
}

func buildKD(pts []kdBuild, axis int, nodes, strs *[]byte) int32 {
	if len(pts) == 0 {
		return -1
	}
	sort.Slice(pts, func(i, j int) bool {
		if axis == 0 {
			if pts[i].x != pts[j].x {
				return pts[i].x < pts[j].x
			}
			return pts[i].y < pts[j].y
		}
		if pts[i].y != pts[j].y {
			return pts[i].y < pts[j].y
		}
		return pts[i].x < pts[j].x
	})
	mid := len(pts) / 2
	idx := int32(len(*nodes) / kdNodeSize)
	*nodes = append(*nodes, make([]byte, kdNodeSize)...)
	left := buildKD(pts[:mid], 1-axis, nodes, strs)
	right := buildKD(pts[mid+1:], 1-axis, nodes, strs)
	p := pts[mid]
	nameOff := uint32(len(*strs))
	*strs = append(*strs, p.name...)
	*strs = append(*strs, p.kind...)
	off := int(idx) * kdNodeSize
	b := (*nodes)[off : off+kdNodeSize]
	binary.LittleEndian.PutUint32(b[0:], uint32(p.x))
	binary.LittleEndian.PutUint32(b[4:], uint32(p.y))
	binary.LittleEndian.PutUint32(b[8:], uint32(left))
	binary.LittleEndian.PutUint32(b[12:], uint32(right))
	binary.LittleEndian.PutUint64(b[16:], uint64(p.osmID))
	binary.LittleEndian.PutUint32(b[24:], nameOff)
	binary.LittleEndian.PutUint16(b[28:], uint16(len(p.name)))
	kl := len(p.kind)
	if kl > 255 {
		kl = 255
	}
	b[30] = byte(kl)
	b[31] = byte(axis)
	return idx
}
