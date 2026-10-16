package geolookup

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
)

const (
	glpMagic    = "GLP1"
	glpVersion  = 1
	glpHeaderN  = 128
	featureSize = 64
	ringRecSize = 8
)

type polyFile struct {
	adminLevel int
	scale      int32
	n          int
	data       []byte
	index      []byte
	feats      []byte
	coords     []byte
	rings      []byte
	strs       []byte
}

func openPolyFile(path string) (*polyFile, func() error, error) {
	data, closer, err := mapFile(path)
	if err != nil {
		return nil, nil, err
	}
	pf, err := parsePoly(data)
	if err != nil {
		_ = closer()
		return nil, nil, err
	}
	return pf, closer, nil
}

func parsePoly(data []byte) (*polyFile, error) {
	if len(data) < glpHeaderN || string(data[:4]) != glpMagic {
		return nil, fmt.Errorf("not a GLP file")
	}
	ver := binary.LittleEndian.Uint32(data[4:])
	if ver != glpVersion {
		return nil, fmt.Errorf("unsupported GLP version %d", ver)
	}
	pf := &polyFile{
		adminLevel: int(int32(binary.LittleEndian.Uint32(data[8:]))),
		n:          int(binary.LittleEndian.Uint32(data[12:])),
		scale:      int32(binary.LittleEndian.Uint32(data[28:])),
		data:       data,
	}
	indexOff := binary.LittleEndian.Uint32(data[32:])
	indexLen := binary.LittleEndian.Uint32(data[36:])
	featOff := binary.LittleEndian.Uint32(data[40:])
	coordOff := binary.LittleEndian.Uint32(data[44:])
	ringOff := binary.LittleEndian.Uint32(data[48:])
	strOff := binary.LittleEndian.Uint32(data[52:])
	if int(indexOff+indexLen) > len(data) || int(featOff)+pf.n*featureSize > len(data) {
		return nil, fmt.Errorf("truncated GLP header offsets")
	}
	pf.index = data[indexOff : indexOff+indexLen]
	pf.feats = data[featOff : featOff+uint32(pf.n*featureSize)]
	pf.coords = data[coordOff:ringOff]
	pf.rings = data[ringOff:strOff]
	pf.strs = data[strOff:]
	return pf, nil
}

func (f *polyFile) query(x, y int32) []Hit {
	var hits []Hit
	rtreeQuery(f.index, x, y, func(idx uint32) {
		if int(idx) >= f.n {
			return
		}
		h, ok := f.hitIfContains(int(idx), x, y)
		if ok {
			hits = append(hits, h)
		}
	})
	return hits
}

func (f *polyFile) hitIfContains(i int, x, y int32) (Hit, bool) {
	off := i * featureSize
	b := f.feats[off : off+featureSize]
	minX := int32(binary.LittleEndian.Uint32(b[16:]))
	minY := int32(binary.LittleEndian.Uint32(b[20:]))
	maxX := int32(binary.LittleEndian.Uint32(b[24:]))
	maxY := int32(binary.LittleEndian.Uint32(b[28:]))
	if !inBBox(x, y, minX, minY, maxX, maxY) {
		return Hit{}, false
	}
	rings := f.featureRings(b)
	if !ringsContain(x, y, rings) {
		return Hit{}, false
	}
	nameOff := binary.LittleEndian.Uint32(b[32:])
	nameLen := binary.LittleEndian.Uint16(b[36:])
	placeLen := binary.LittleEndian.Uint16(b[38:])
	var country string
	if b[50] != 0 {
		country = string(b[50:52])
	}
	name, place := "", ""
	if int(nameOff)+int(nameLen)+int(placeLen) <= len(f.strs) {
		name = string(f.strs[nameOff : nameOff+uint32(nameLen)])
		place = string(f.strs[nameOff+uint32(nameLen) : nameOff+uint32(nameLen)+uint32(placeLen)])
	}
	return Hit{
		AdminLevel: f.adminLevel,
		Name:       name,
		Place:      place,
		OSMID:      int64(binary.LittleEndian.Uint64(b[0:])),
		Source:     SourcePolygon,
		Area:       binary.LittleEndian.Uint64(b[8:]),
		Country:    country,
	}, true
}

func (f *polyFile) featureRings(b []byte) []ring {
	coordStart := binary.LittleEndian.Uint32(b[40:])
	ringStart := binary.LittleEndian.Uint32(b[44:])
	ringCount := binary.LittleEndian.Uint16(b[48:])
	out := make([]ring, 0, ringCount)
	for i := 0; i < int(ringCount); i++ {
		ro := (int(ringStart) + i) * ringRecSize
		if ro+ringRecSize > len(f.rings) {
			break
		}
		start := binary.LittleEndian.Uint32(f.rings[ro:])
		count := binary.LittleEndian.Uint16(f.rings[ro+4:])
		hole := f.rings[ro+6] != 0
		pts := make([]qpoint, count)
		for j := 0; j < int(count); j++ {
			co := (int(coordStart) + int(start) + j) * 8
			if co+8 > len(f.coords) {
				break
			}
			pts[j] = qpoint{
				X: int32(binary.LittleEndian.Uint32(f.coords[co:])),
				Y: int32(binary.LittleEndian.Uint32(f.coords[co+4:])),
			}
		}
		out = append(out, ring{pts: pts, hole: hole})
	}
	return out
}

func writePolyFile(path string, adminLevel int, scale int32, feats []packedPoly) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	items := make([]rtreeItem, len(feats))
	for i, p := range feats {
		var pts []qpoint
		for _, r := range p.rings {
			pts = append(pts, r.pts...)
		}
		minX, minY, maxX, maxY := bboxOf(pts)
		items[i] = rtreeItem{minX, minY, maxX, maxY, i}
	}
	order := sortHilbert(items)
	sorted := make([]packedPoly, len(feats))
	leaves := make([]rtreeItem, len(feats))
	for i, oi := range order {
		sorted[i] = feats[oi]
		it := items[oi]
		it.index = i
		leaves[i] = it
	}
	index := flattenRTree(leaves)

	var coordBuf, ringBuf, strBuf []byte
	featBuf := make([]byte, len(sorted)*featureSize)
	for i, p := range sorted {
		nameOff := uint32(len(strBuf))
		strBuf = append(strBuf, p.name...)
		strBuf = append(strBuf, p.place...)
		coordStart := uint32(len(coordBuf) / 8)
		ringStart := uint32(len(ringBuf) / ringRecSize)
		var localCoord uint32
		for _, r := range p.rings {
			rec := make([]byte, ringRecSize)
			binary.LittleEndian.PutUint32(rec, localCoord)
			binary.LittleEndian.PutUint16(rec[4:], uint16(len(r.pts)))
			if r.hole {
				rec[6] = 1
			}
			ringBuf = append(ringBuf, rec...)
			for _, pt := range r.pts {
				var c [8]byte
				binary.LittleEndian.PutUint32(c[0:], uint32(pt.X))
				binary.LittleEndian.PutUint32(c[4:], uint32(pt.Y))
				coordBuf = append(coordBuf, c[:]...)
			}
			localCoord += uint32(len(r.pts))
		}
		var pts []qpoint
		for _, r := range p.rings {
			pts = append(pts, r.pts...)
		}
		minX, minY, maxX, maxY := bboxOf(pts)
		off := i * featureSize
		binary.LittleEndian.PutUint64(featBuf[off:], uint64(p.osmID))
		binary.LittleEndian.PutUint64(featBuf[off+8:], polygonArea2(p.rings))
		binary.LittleEndian.PutUint32(featBuf[off+16:], uint32(minX))
		binary.LittleEndian.PutUint32(featBuf[off+20:], uint32(minY))
		binary.LittleEndian.PutUint32(featBuf[off+24:], uint32(maxX))
		binary.LittleEndian.PutUint32(featBuf[off+28:], uint32(maxY))
		binary.LittleEndian.PutUint32(featBuf[off+32:], nameOff)
		binary.LittleEndian.PutUint16(featBuf[off+36:], uint16(len(p.name)))
		binary.LittleEndian.PutUint16(featBuf[off+38:], uint16(len(p.place)))
		binary.LittleEndian.PutUint32(featBuf[off+40:], coordStart)
		binary.LittleEndian.PutUint32(featBuf[off+44:], ringStart)
		binary.LittleEndian.PutUint16(featBuf[off+48:], uint16(len(p.rings)))
		cc := p.country
		if len(cc) >= 2 {
			featBuf[off+50] = cc[0]
			featBuf[off+51] = cc[1]
		}
	}

	indexOff := uint32(glpHeaderN)
	featOff := indexOff + uint32(len(index))
	coordOff := featOff + uint32(len(featBuf))
	ringOff := coordOff + uint32(len(coordBuf))
	strOff := ringOff + uint32(len(ringBuf))

	hdr := make([]byte, glpHeaderN)
	copy(hdr, glpMagic)
	binary.LittleEndian.PutUint32(hdr[4:], glpVersion)
	binary.LittleEndian.PutUint32(hdr[8:], uint32(adminLevel))
	binary.LittleEndian.PutUint32(hdr[12:], uint32(len(sorted)))
	binary.LittleEndian.PutUint32(hdr[16:], uint32(len(coordBuf)/8))
	binary.LittleEndian.PutUint32(hdr[20:], uint32(len(ringBuf)/ringRecSize))
	binary.LittleEndian.PutUint32(hdr[24:], uint32(len(strBuf)))
	binary.LittleEndian.PutUint32(hdr[28:], uint32(scale))
	binary.LittleEndian.PutUint32(hdr[32:], indexOff)
	binary.LittleEndian.PutUint32(hdr[36:], uint32(len(index)))
	binary.LittleEndian.PutUint32(hdr[40:], featOff)
	binary.LittleEndian.PutUint32(hdr[44:], coordOff)
	binary.LittleEndian.PutUint32(hdr[48:], ringOff)
	binary.LittleEndian.PutUint32(hdr[52:], strOff)

	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()
	for _, chunk := range [][]byte{hdr, index, featBuf, coordBuf, ringBuf, strBuf} {
		if _, err := out.Write(chunk); err != nil {
			return err
		}
	}
	return nil
}
