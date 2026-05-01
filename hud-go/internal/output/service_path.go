package output

import (
	"container/heap"
	"math"

	"hud-go/internal/geo"
	"hud-go/internal/pak/uasset"
)

// ServiceCoord matches hud-go's coordinates[] schema. We populate
// Latitude/Longitude/Height; recording-only fields (gradient, timestamp,
// gameTime) stay omitted because they don't apply to static extraction.
type ServiceCoord struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Height    float64 `json:"height,omitempty"`
}

// BuildServicePath resolves the train's actual physical path along the rail
// network for a service. Walks each consecutive pair of schedule waypoints
// through the ribbon graph (Dijkstra weighted by ribbon length), samples
// arc points along every ribbon in the chain, and returns a continuous
// lat/lng list ready to drop into the per-service JSON's coordinates[].
//
// Returns nil when the schedule has nothing resolvable (no ribbons, missing
// origin, etc.). Otherwise the list always starts at the first waypoint
// and ends at the last.
func BuildServicePath(svc *uasset.Service, ribbons map[string]*uasset.Ribbon, anchor *geo.RouteAnchor) []ServiceCoord {
	if anchor == nil || len(ribbons) == 0 {
		return nil
	}
	// Collect schedule waypoints with a usable ribbon ref. Each becomes a
	// (ribbon, fraction-along-ribbon) anchor we walk between.
	type waypoint struct {
		ribbon   *uasset.Ribbon
		fraction float64
	}
	wps := make([]waypoint, 0, len(svc.Schedule))
	for _, it := range svc.Schedule {
		if it.RibbonGUID == "" {
			continue
		}
		key := uasset.NormalizeGUID(it.RibbonGUID)
		if key == "" {
			key = it.RibbonGUID
		}
		rib, ok := ribbons[key]
		if !ok || rib == nil || rib.Length <= 0 {
			continue
		}
		f := float64(it.RibbonLocation)
		if f < 0 {
			f = 0
		}
		if f > 1 {
			f = 1
		}
		wps = append(wps, waypoint{ribbon: rib, fraction: f})
	}
	if len(wps) == 0 {
		return nil
	}

	adj := buildNodeAdjacency(ribbons)
	out := []ServiceCoord{}
	// Seed with the first waypoint's position so a service that has just one
	// resolvable schedule item still returns a single dot.
	out = append(out, ribbonPointAt(wps[0].ribbon, wps[0].fraction, anchor))

	for i := 1; i < len(wps); i++ {
		prev, cur := wps[i-1], wps[i]
		seg := samplePathBetween(adj, ribbons, anchor, prev.ribbon, prev.fraction, cur.ribbon, cur.fraction)
		// Drop the first point of every segment after the first — it's the
		// shared waypoint with the previous segment's last point.
		if len(seg) > 0 {
			seg = seg[1:]
		}
		out = append(out, seg...)
	}
	return out
}

// buildNodeAdjacency returns node_guid -> []edgeRole, mirroring the merged-rails
// helper but local to this file.
func buildNodeAdjacency(ribbons map[string]*uasset.Ribbon) map[string][]edgeRole {
	adj := map[string][]edgeRole{}
	for _, r := range ribbons {
		if r.Length <= 0 {
			continue
		}
		if r.StartNodeGUID != "" {
			adj[r.StartNodeGUID] = append(adj[r.StartNodeGUID], edgeRole{r, "start"})
		}
		if r.EndNodeGUID != "" {
			adj[r.EndNodeGUID] = append(adj[r.EndNodeGUID], edgeRole{r, "end"})
		}
	}
	return adj
}

// samplePathBetween walks from (prevRib, prevFrac) to (curRib, curFrac).
// If they're on the same ribbon, we sample the segment between the two
// fractions directly. Otherwise we Dijkstra on the ribbon graph using length
// as edge weight, then concatenate samples along the chain (partial start
// ribbon → whole intermediate ribbons → partial end ribbon).
func samplePathBetween(adj map[string][]edgeRole, ribbons map[string]*uasset.Ribbon, anchor *geo.RouteAnchor, prevRib *uasset.Ribbon, prevFrac float64, curRib *uasset.Ribbon, curFrac float64) []ServiceCoord {
	if prevRib.GUID == curRib.GUID {
		return sampleRibbonRange(prevRib, prevFrac, curFrac, anchor)
	}
	chain := dijkstraRibbonPath(adj, ribbons, prevRib, curRib)
	if chain == nil {
		// No graph path — fall back to a straight line between the two
		// waypoints. Better than dropping the segment silently.
		return []ServiceCoord{
			ribbonPointAt(prevRib, prevFrac, anchor),
			ribbonPointAt(curRib, curFrac, anchor),
		}
	}
	out := []ServiceCoord{}
	for i, e := range chain {
		switch i {
		case 0:
			// First ribbon: from prevFrac to whichever end leads to the next.
			endFrac := 1.0
			if e.role == "end" {
				endFrac = 0.0
			}
			out = append(out, sampleRibbonRange(e.rib, prevFrac, endFrac, anchor)...)
		case len(chain) - 1:
			// Last ribbon: from start (whichever end we entered from) to curFrac.
			startFrac := 0.0
			if e.role == "end" {
				startFrac = 1.0
			}
			seg := sampleRibbonRange(e.rib, startFrac, curFrac, anchor)
			if len(seg) > 0 && len(out) > 0 {
				seg = seg[1:] // drop dup at join
			}
			out = append(out, seg...)
		default:
			// Intermediate ribbon: traverse fully in role direction.
			startFrac, endFrac := 0.0, 1.0
			if e.role == "end" {
				startFrac, endFrac = 1.0, 0.0
			}
			seg := sampleRibbonRange(e.rib, startFrac, endFrac, anchor)
			if len(seg) > 0 && len(out) > 0 {
				seg = seg[1:] // drop dup at join
			}
			out = append(out, seg...)
		}
	}
	return out
}

// sampleRibbonRange samples points along a ribbon from fraction `from` to
// fraction `to` (each in [0,1]). Reuses the curvature-aware step from the
// rails writer. The first sample is exactly at `from`, the last exactly at
// `to`.
func sampleRibbonRange(rib *uasset.Ribbon, from, to float64, anchor *geo.RouteAnchor) []ServiceCoord {
	if rib.Length <= 0 {
		return nil
	}
	stepCm := 5.0 * 100.0 // 5 m default
	delta := math.Abs(to - from)
	if delta == 0 {
		return []ServiceCoord{ribbonPointAt(rib, from, anchor)}
	}
	rangeCm := delta * rib.Length
	n := int(math.Ceil(rangeCm / stepCm))
	// Curvature refinement for tight-radius ribbons.
	if rib.Radius != 0 && !math.IsInf(rib.Radius, 0) && !math.IsNaN(rib.Radius) {
		curveN := int(math.Ceil(rangeCm / (math.Abs(rib.Radius) * maxDeflectionRadians)))
		if curveN > n {
			n = curveN
		}
	}
	if n < 1 {
		n = 1
	}
	if n > 400 {
		n = 400
	}
	out := make([]ServiceCoord, 0, n+1)
	for i := 0; i <= n; i++ {
		t := float64(i) / float64(n)
		f := from + t*(to-from)
		out = append(out, ribbonPointAt(rib, f, anchor))
	}
	return out
}

// ribbonPointAt returns the world lat/lng of a ribbon at the given fraction
// (0..1 along its length). Includes Z (height) from CachedStartPosition
// when available — useful for 3D rail profiles later.
func ribbonPointAt(rib *uasset.Ribbon, fraction float64, anchor *geo.RouteAnchor) ServiceCoord {
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	atLen := fraction * rib.Length
	dx, dy := geo.ArcDelta(0, 0, rib.TangentX, rib.TangentY, rib.Radius, atLen)
	originX, originY, originZ := ribbonWorldOrigin(rib)
	lat, lng := anchor.WorldToLatLng((originX+dx)/100.0, (originY+dy)/100.0)
	return ServiceCoord{
		Latitude:  round7(lat),
		Longitude: round7(lng),
		Height:    round1(originZ / 100.0),
	}
}

// dijkstraRibbonPath finds the shortest sequence of ribbons from start to
// end using ribbon length (m) as edge weight. Returns the chain as a slice
// of edgeRole indicating each ribbon and the role at the SHARED node where
// the previous ribbon connected — i.e. e.role == "start" means we entered
// this ribbon at its start_node, so we traverse start→end.
//
// Returns nil when no path exists.
func dijkstraRibbonPath(adj map[string][]edgeRole, ribbons map[string]*uasset.Ribbon, start, end *uasset.Ribbon) []edgeRole {
	if start == nil || end == nil {
		return nil
	}
	if start.GUID == end.GUID {
		return []edgeRole{{rib: start, role: "start"}}
	}

	// Multi-source: we can leave `start` from either of its two nodes. Same
	// for `end`. Run Dijkstra over (ribbon, entry_role) state — types
	// declared at package level so the heap.Interface methods see them.
	pq := &priorityQueue{}
	heap.Init(pq)
	dist := map[stateKey]float64{}
	prev := map[stateKey]stateKey{}
	visited := map[stateKey]bool{}

	push := func(k stateKey, d float64) {
		if existing, ok := dist[k]; ok && d >= existing {
			return
		}
		dist[k] = d
		heap.Push(pq, &heapItem{key: k, dist: d})
	}

	// Seed with start ribbon entered from each end (we need to leave through
	// the OTHER end, so cost = full ribbon length either way).
	startKeys := []stateKey{
		{guid: start.GUID, role: "start"},
		{guid: start.GUID, role: "end"},
	}
	for _, k := range startKeys {
		push(k, 0)
	}

	endKey := stateKey{}
	for !pq.empty() {
		it := heap.Pop(pq).(*heapItem)
		if visited[it.key] {
			continue
		}
		visited[it.key] = true

		if it.key.guid == end.GUID {
			endKey = it.key
			break
		}

		curRib := ribbons[lookupKey(it.key.guid, ribbons)]
		if curRib == nil {
			continue
		}
		// Exit node: opposite of entry role.
		var exitNode string
		if it.key.role == "start" {
			exitNode = curRib.EndNodeGUID
		} else {
			exitNode = curRib.StartNodeGUID
		}
		if exitNode == "" {
			continue
		}
		newDist := it.dist + curRib.Length/100.0 // metres
		for _, e := range adj[exitNode] {
			if e.rib.GUID == curRib.GUID {
				continue
			}
			next := stateKey{guid: e.rib.GUID, role: e.role}
			if visited[next] {
				continue
			}
			if existing, ok := dist[next]; ok && newDist >= existing {
				continue
			}
			dist[next] = newDist
			prev[next] = it.key
			heap.Push(pq, &heapItem{key: next, dist: newDist})
		}
	}

	if endKey.guid == "" {
		return nil
	}
	// Walk back via prev to reconstruct the chain.
	chain := []edgeRole{}
	cur := endKey
	for {
		rib := ribbons[lookupKey(cur.guid, ribbons)]
		if rib == nil {
			break
		}
		chain = append([]edgeRole{{rib: rib, role: cur.role}}, chain...)
		p, ok := prev[cur]
		if !ok {
			break
		}
		cur = p
	}
	return chain
}

// lookupKey normalises a fmtGUID-style GUID to the canonical map key.
func lookupKey(guid string, ribbons map[string]*uasset.Ribbon) string {
	if _, ok := ribbons[guid]; ok {
		return guid
	}
	if k := uasset.NormalizeGUID(guid); k != "" {
		if _, ok := ribbons[k]; ok {
			return k
		}
	}
	return guid
}

// priorityQueue is a tiny min-heap on heapItem.dist.
type priorityQueue []*heapItem

type heapItem struct {
	key  stateKey
	dist float64
}

type stateKey struct {
	guid string
	role string
}

func (pq priorityQueue) Len() int            { return len(pq) }
func (pq priorityQueue) Less(i, j int) bool  { return pq[i].dist < pq[j].dist }
func (pq priorityQueue) Swap(i, j int)       { pq[i], pq[j] = pq[j], pq[i] }
func (pq *priorityQueue) Push(x any)         { *pq = append(*pq, x.(*heapItem)) }
func (pq *priorityQueue) Pop() any {
	old := *pq
	n := len(old)
	it := old[n-1]
	*pq = old[:n-1]
	return it
}
func (pq *priorityQueue) empty() bool { return len(*pq) == 0 }
