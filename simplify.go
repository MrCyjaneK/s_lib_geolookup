package geolookup

import (
	"container/heap"
	"runtime"
	"sync"
)

type packedPoly struct {
	osmID                int64
	level                int
	name, place, country string
	rings                []ring
}

type vertexKey struct{ X, Y int32 }

type undirected struct{ A, B vertexKey }

func undir(a, b qpoint) undirected {
	ka, kb := vertexKey{a.X, a.Y}, vertexKey{b.X, b.Y}
	if ka.X < kb.X || (ka.X == kb.X && ka.Y < kb.Y) {
		return undirected{ka, kb}
	}
	return undirected{kb, ka}
}

func qkey(p qpoint) vertexKey { return vertexKey{p.X, p.Y} }

func closeRing(pts []qpoint) []qpoint {
	if len(pts) == 0 {
		return pts
	}
	out := make([]qpoint, 0, len(pts)+1)
	out = append(out, pts[0])
	for i := 1; i < len(pts); i++ {
		if pts[i] != pts[i-1] {
			out = append(out, pts[i])
		}
	}
	if out[0] != out[len(out)-1] {
		out = append(out, out[0])
	}
	return out
}

// simplifyTopology runs Visvalingam-Whyatt once per shared arc so adjacent
// polygons keep a common edge (no per-polygon Douglas-Peucker gaps).
// jobs controls parallel VW over independent arcs (≤0 → GOMAXPROCS).
func simplifyTopology(polys []packedPoly, minArea uint64, jobs int) {
	if jobs <= 0 {
		jobs = runtime.GOMAXPROCS(0)
	}
	adj := map[vertexKey][]qpoint{}
	addEdge := func(a, b qpoint) {
		ka, kb := qkey(a), qkey(b)
		if ka == kb {
			return
		}
		adj[ka] = appendUnique(adj[ka], b)
		adj[kb] = appendUnique(adj[kb], a)
	}
	for _, p := range polys {
		for _, r := range p.rings {
			pts := r.pts
			n := len(pts)
			if n < 2 {
				continue
			}
			if pts[0] == pts[n-1] {
				n--
			}
			for i := 0; i < n; i++ {
				addEdge(pts[i], pts[(i+1)%n])
			}
		}
	}
	degree := func(p qpoint) int { return len(adj[qkey(p)]) }

	type dirKey struct {
		A, B vertexKey
	}
	type arcRef struct {
		id, pos, segs int
		fwd           bool
	}
	dir := map[dirKey]arcRef{}
	type arc struct{ pts []qpoint }

	used := map[dirKey]bool{}
	var arcs []arc
	walk := func(start, next qpoint) {
		dk := dirKey{qkey(start), qkey(next)}
		if used[dk] {
			return
		}
		if _, ok := dir[dk]; ok {
			return
		}
		chain := []qpoint{start, next}
		used[dk] = true
		prev, cur := start, next
		for degree(cur) == 2 {
			nb := adj[qkey(cur)]
			var nxt qpoint
			if qkey(nb[0]) == qkey(prev) {
				nxt = nb[1]
			} else {
				nxt = nb[0]
			}
			if qkey(nxt) == qkey(start) && qkey(prev) != qkey(start) {
				chain = append(chain, nxt)
				used[dirKey{qkey(cur), qkey(nxt)}] = true
				cur = nxt
				break
			}
			used[dirKey{qkey(cur), qkey(nxt)}] = true
			chain = append(chain, nxt)
			prev, cur = cur, nxt
			if len(chain) > 1_000_000 {
				break
			}
		}
		id := len(arcs)
		arcs = append(arcs, arc{chain})
		segs := len(chain) - 1
		for i := 0; i < segs; i++ {
			a, b := chain[i], chain[i+1]
			dir[dirKey{qkey(a), qkey(b)}] = arcRef{id, i, segs, true}
			dir[dirKey{qkey(b), qkey(a)}] = arcRef{id, segs - 1 - i, segs, false}
		}
	}
	for k, nbs := range adj {
		a := qpoint{k.X, k.Y}
		if degree(a) != 2 {
			for _, b := range nbs {
				walk(a, b)
			}
		}
	}
	// Pure cycles (every vertex degree 2).
	for k, nbs := range adj {
		a := qpoint{k.X, k.Y}
		if len(nbs) == 0 {
			continue
		}
		walk(a, nbs[0])
	}

	if minArea > 0 && len(arcs) > 0 {
		if jobs < 1 {
			jobs = 1
		}
		if jobs > len(arcs) {
			jobs = len(arcs)
		}
		var wg sync.WaitGroup
		ch := make(chan int, jobs*2)
		for w := 0; w < jobs; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range ch {
					arcs[i].pts = visvalingam(arcs[i].pts, minArea)
				}
			}()
		}
		for i := range arcs {
			ch <- i
		}
		close(ch)
		wg.Wait()
	}

	for pi := range polys {
		for ri, r := range polys[pi].rings {
			pts := r.pts
			n := len(pts)
			if n < 2 {
				continue
			}
			closed := pts[0] == pts[n-1]
			if closed {
				n--
			}
			startAt := 0
			for i := 0; i < n; i++ {
				ref, ok := dir[dirKey{qkey(pts[i]), qkey(pts[(i+1)%n])}]
				if ok && ref.pos == 0 {
					startAt = i
					break
				}
			}
			var out []qpoint
			for seen := 0; seen < n; {
				i := (startAt + seen) % n
				a, b := pts[i], pts[(i+1)%n]
				ref, ok := dir[dirKey{qkey(a), qkey(b)}]
				if !ok || ref.pos != 0 {
					if !ok {
						out = append(out, a)
					}
					seen++
					continue
				}
				seq := arcs[ref.id].pts
				if !ref.fwd {
					seq = reversePts(seq)
				}
				if len(out) == 0 {
					out = append(out, seq...)
				} else if len(seq) > 0 {
					out = append(out, seq[1:]...)
				}
				seen += max(1, ref.segs)
			}
			if len(out) > 0 && (closed || qkey(out[0]) != qkey(out[len(out)-1])) {
				if qkey(out[0]) != qkey(out[len(out)-1]) {
					out = append(out, out[0])
				}
			}
			polys[pi].rings[ri].pts = closeRing(out)
		}
	}
}

func appendUnique(s []qpoint, p qpoint) []qpoint {
	for _, q := range s {
		if q == p {
			return s
		}
	}
	return append(s, p)
}

func reversePts(pts []qpoint) []qpoint {
	out := make([]qpoint, len(pts))
	for i := range pts {
		out[len(pts)-1-i] = pts[i]
	}
	return out
}

type vwItem struct {
	i, ver int
	area   uint64
}

type vwHeap []vwItem

func (h vwHeap) Len() int           { return len(h) }
func (h vwHeap) Less(i, j int) bool { return h[i].area < h[j].area }
func (h vwHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *vwHeap) Push(x any)        { *h = append(*h, x.(vwItem)) }
func (h *vwHeap) Pop() any          { n := len(*h); x := (*h)[n-1]; *h = (*h)[:n-1]; return x }

func visvalingam(pts []qpoint, minArea uint64) []qpoint {
	if len(pts) <= 2 {
		return pts
	}
	cyclic := pts[0] == pts[len(pts)-1]
	work := append([]qpoint(nil), pts...)
	if cyclic && len(work) > 1 {
		work = work[:len(work)-1]
	}
	nKeep := 2
	if cyclic {
		nKeep = 3
	}
	if len(work) <= nKeep {
		if cyclic {
			return closeRing(work)
		}
		return work
	}
	prev := make([]int, len(work))
	next := make([]int, len(work))
	ver := make([]int, len(work))
	alive := make([]bool, len(work))
	for i := range work {
		prev[i] = i - 1
		next[i] = i + 1
		alive[i] = true
	}
	if cyclic {
		prev[0] = len(work) - 1
		next[len(work)-1] = 0
	} else {
		prev[0] = -1
		next[len(work)-1] = -1
	}
	areaOf := func(i int) uint64 {
		p, n := prev[i], next[i]
		if p < 0 || n < 0 {
			return ^uint64(0)
		}
		return uint64(abs64(int64(work[i].X-work[p].X)*int64(work[n].Y-work[p].Y) -
			int64(work[i].Y-work[p].Y)*int64(work[n].X-work[p].X)))
	}
	h := &vwHeap{}
	heap.Init(h)
	push := func(i int) {
		if i < 0 || (!cyclic && (prev[i] < 0 || next[i] < 0)) {
			return
		}
		ver[i]++
		heap.Push(h, vwItem{i, ver[i], areaOf(i)})
	}
	if cyclic {
		for i := range work {
			push(i)
		}
	} else {
		for i := 1; i < len(work)-1; i++ {
			push(i)
		}
	}
	aliveN := len(work)
	for h.Len() > 0 && aliveN > nKeep {
		it := heap.Pop(h).(vwItem)
		if !alive[it.i] || it.ver != ver[it.i] {
			continue
		}
		if it.area >= minArea {
			break
		}
		alive[it.i] = false
		aliveN--
		p, n := prev[it.i], next[it.i]
		if p >= 0 {
			next[p] = n
		}
		if n >= 0 {
			prev[n] = p
		}
		push(p)
		push(n)
	}
	out := make([]qpoint, 0, aliveN)
	start := 0
	if !cyclic {
		start = 0
	}
	for i := start; i < len(work); i++ {
		if alive[i] {
			out = append(out, work[i])
		}
	}
	if cyclic {
		return closeRing(out)
	}
	return out
}
