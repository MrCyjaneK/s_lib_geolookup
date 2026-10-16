package geolookup

import (
	"encoding/binary"
	"sort"
)

const (
	rtreeFanout    = 16
	rtreeEntrySize = 24 // bbox (16) + start (4) + count (4); count==0 means empty
)

type rtreeItem struct {
	minX, minY, maxX, maxY int32
	index                  int
}

func sortHilbert(items []rtreeItem) []int {
	n := len(items)
	order := make([]int, n)
	if n == 0 {
		return order
	}
	minX, minY, maxX, maxY := items[0].minX, items[0].minY, items[0].maxX, items[0].maxY
	for _, it := range items[1:] {
		if it.minX < minX {
			minX = it.minX
		}
		if it.minY < minY {
			minY = it.minY
		}
		if it.maxX > maxX {
			maxX = it.maxX
		}
		if it.maxY > maxY {
			maxY = it.maxY
		}
	}
	dx := uint64(int64(maxX) - int64(minX))
	dy := uint64(int64(maxY) - int64(minY))
	if dx == 0 {
		dx = 1
	}
	if dy == 0 {
		dy = 1
	}
	const nbits = uint32(1 << 16)
	type keyed struct {
		h uint32
		i int
	}
	keys := make([]keyed, n)
	for i, it := range items {
		cx := uint64(int64(it.minX)+int64(it.maxX))/2 - uint64(int64(minX))
		cy := uint64(int64(it.minY)+int64(it.maxY))/2 - uint64(int64(minY))
		hx := uint32(cx * uint64(nbits-1) / dx)
		hy := uint32(cy * uint64(nbits-1) / dy)
		keys[i] = keyed{hilbertIndex(nbits, hx, hy), i}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].h != keys[j].h {
			return keys[i].h < keys[j].h
		}
		return keys[i].i < keys[j].i
	})
	for i, k := range keys {
		order[i] = k.i
	}
	return order
}

// flattenRTree builds a root-first packed tree over Hilbert-sorted leaf items.
// Each node has rtreeFanout slots. Leaf slots store feature indices in child/start
// with count=1. Internal slots store the child node index and count=fanout (unused).
// Empty slots have count=0.
//
// Layout per slot: minX minY maxX maxY int32, start uint32, count uint32.
// Leaf: start=featureIndex, count=1.
// Internal: start=childNodeIndex, count=2 (marker).
func flattenRTree(leaves []rtreeItem) []byte {
	if len(leaves) == 0 {
		return nil
	}
	type slot struct {
		minX, minY, maxX, maxY int32
		start, count           uint32
	}
	fillNode := func(items []slot) []slot {
		out := make([]slot, rtreeFanout)
		copy(out, items)
		return out
	}

	// Bottom: one slot per feature.
	cur := make([]slot, len(leaves))
	for i, it := range leaves {
		cur[i] = slot{it.minX, it.minY, it.maxX, it.maxY, uint32(it.index), 1}
	}

	var levels [][][]slot // level[0] is leaves grouped into nodes
	for {
		nodes := make([][]slot, 0, (len(cur)+rtreeFanout-1)/rtreeFanout)
		for i := 0; i < len(cur); i += rtreeFanout {
			hi := i + rtreeFanout
			if hi > len(cur) {
				hi = len(cur)
			}
			nodes = append(nodes, fillNode(cur[i:hi]))
		}
		levels = append(levels, nodes)
		if len(nodes) == 1 {
			break
		}
		next := make([]slot, len(nodes))
		for i, nd := range nodes {
			s := nd[0]
			for _, e := range nd[1:] {
				if e.count == 0 {
					continue
				}
				if e.minX < s.minX {
					s.minX = e.minX
				}
				if e.minY < s.minY {
					s.minY = e.minY
				}
				if e.maxX > s.maxX {
					s.maxX = e.maxX
				}
				if e.maxY > s.maxY {
					s.maxY = e.maxY
				}
			}
			s.count = 2
			s.start = uint32(i) // filled in after we know node indices
			next[i] = s
		}
		cur = next
	}

	// Assign absolute node indices, root first.
	indexOf := make([][]int, len(levels))
	idx := 0
	for li := len(levels) - 1; li >= 0; li-- {
		indexOf[li] = make([]int, len(levels[li]))
		for i := range levels[li] {
			indexOf[li][i] = idx
			idx++
		}
	}
	// Internal levels (all except 0) point at child nodes of the previous finer level.
	for li := 1; li < len(levels); li++ {
		for ni := range levels[li] {
			// this node covers children levels[li-1][ni*fanout : ...]
			// After grouping, levels[li][ni] corresponds to child group ni on level li-1.
			for ei := 0; ei < rtreeFanout; ei++ {
				if levels[li][ni][ei].count == 0 {
					continue
				}
				child := ni*rtreeFanout + ei
				if child < len(indexOf[li-1]) {
					levels[li][ni][ei].start = uint32(indexOf[li-1][child])
					levels[li][ni][ei].count = 2
				}
			}
		}
	}

	ordered := make([][]slot, idx)
	for li := len(levels) - 1; li >= 0; li-- {
		for i, nd := range levels[li] {
			ordered[indexOf[li][i]] = nd
		}
	}
	buf := make([]byte, len(ordered)*rtreeFanout*rtreeEntrySize)
	for ni, nd := range ordered {
		base := ni * rtreeFanout * rtreeEntrySize
		for ei := 0; ei < rtreeFanout; ei++ {
			off := base + ei*rtreeEntrySize
			e := nd[ei]
			binary.LittleEndian.PutUint32(buf[off:], uint32(e.minX))
			binary.LittleEndian.PutUint32(buf[off+4:], uint32(e.minY))
			binary.LittleEndian.PutUint32(buf[off+8:], uint32(e.maxX))
			binary.LittleEndian.PutUint32(buf[off+12:], uint32(e.maxY))
			binary.LittleEndian.PutUint32(buf[off+16:], e.start)
			binary.LittleEndian.PutUint32(buf[off+20:], e.count)
		}
	}
	return buf
}

func rtreeQuery(index []byte, x, y int32, visit func(featureIdx uint32)) {
	if len(index) < rtreeFanout*rtreeEntrySize {
		return
	}
	nodeBytes := rtreeFanout * rtreeEntrySize
	var walk func(nodeIdx int)
	walk = func(nodeIdx int) {
		base := nodeIdx * nodeBytes
		if base < 0 || base+nodeBytes > len(index) {
			return
		}
		for i := 0; i < rtreeFanout; i++ {
			off := base + i*rtreeEntrySize
			count := binary.LittleEndian.Uint32(index[off+20:])
			if count == 0 {
				continue
			}
			minX := int32(binary.LittleEndian.Uint32(index[off:]))
			minY := int32(binary.LittleEndian.Uint32(index[off+4:]))
			maxX := int32(binary.LittleEndian.Uint32(index[off+8:]))
			maxY := int32(binary.LittleEndian.Uint32(index[off+12:]))
			if !inBBox(x, y, minX, minY, maxX, maxY) {
				continue
			}
			start := binary.LittleEndian.Uint32(index[off+16:])
			if count == 1 {
				visit(start)
				continue
			}
			walk(int(start))
		}
	}
	walk(0)
}
