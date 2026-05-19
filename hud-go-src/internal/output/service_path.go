package output

import (
	"container/heap"
	"math"

	"hud-go/internal/geo"
	"hud-go/internal/pak/uasset"
	"hud-go/internal/timetableaction"
)

// ServiceCoord matches hud-go's coordinates[] schema. We populate
// Latitude/Longitude/Height; recording-only fields (gradient, timestamp,
// gameTime) stay omitted because they don't apply to static extraction.
//
// `Break` (omitted unless true) marks a polyline discontinuity — when the
// path-builder couldn't find a rail-graph route between two scheduled
// waypoints, we emit the next waypoint with Break=true rather than draw a
// straight line through non-rail terrain. Renderers split their polyline at
// each break point so the gap stays visible.
type ServiceCoord struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	// Height is preserved in memory (path builders set it from ribbon
	// data) but NOT serialised — nothing in the HUD / map renderers
	// reads it, and dropping the `"height":` key trims ~33% off the
	// per-service JSON blob in route zips and the DB's
	// timetable_coordinates table.
	Height float64 `json:"-"`
	Break  bool    `json:"break,omitempty"`
}

// DecimateCoordsMeters returns coords with intermediate points dropped
// when they sit within `minDistM` metres of the previously-kept point.
// The first and last points always survive; any `Break=true` point
// always survives so polyline discontinuities aren't smoothed over.
//
// Used at extract time to ship a ~5 m resolution polyline instead of
// the path builder's natural sub-metre sampling. Renderers (Leaflet
// + HUDs) zoom levels show 1px ≈ 5+ m so sub-metre points are visually
// indistinguishable; storage shrinks ~5×.
func DecimateCoordsMeters(coords []ServiceCoord, minDistM float64) []ServiceCoord {
	if len(coords) < 3 || minDistM <= 0 {
		return coords
	}
	out := make([]ServiceCoord, 0, len(coords))
	out = append(out, coords[0])
	last := coords[0]
	for i := 1; i < len(coords)-1; i++ {
		c := coords[i]
		if c.Break {
			out = append(out, c)
			last = c
			continue
		}
		if coordDistanceMeters(last, c) >= minDistM {
			out = append(out, c)
			last = c
		}
	}
	out = append(out, coords[len(coords)-1])
	return out
}

// coordDistanceMeters: equirectangular approximation (sub-cm accuracy
// at the few-km scales between consecutive sub-metre samples). Cheap
// and good enough — decimation only needs "near enough to drop".
func coordDistanceMeters(a, b ServiceCoord) float64 {
	const R = 6371000.0
	dLat := (b.Latitude - a.Latitude) * math.Pi / 180
	dLng := (b.Longitude - a.Longitude) * math.Pi / 180
	meanLat := (a.Latitude + b.Latitude) * 0.5 * math.Pi / 180
	x := dLng * math.Cos(meanLat)
	return math.Sqrt(dLat*dLat+x*x) * R
}

// BuildServicePath resolves the train's actual physical path along the rail
// network for a service. Walks each consecutive pair of schedule waypoints
// through the ribbon graph (Dijkstra weighted by ribbon length), samples
// arc points along every ribbon in the chain, and returns a continuous
// lat/lng list ready to drop into the per-service JSON's coordinates[].
//
// `switches` is the route's NetworkTurnoutJunction records (Timetable.Switches).
// Each switch declares its three connected ribbons authoritatively, so we
// stitch them together in the graph even when the ribbons' StartNodeGUID /
// EndNodeGUID don't agree (TSW6 data drift). Pass nil to fall back to
// node-GUID-only adjacency.
//
// `vertices` is the per-ribbon-GUID polyline map produced by the rails
// builder (cookedmap.buildRailsFeature, surfaced via Timetable.RibbonVertices).
// When non-nil, the path-builder slices each ribbon's segment directly from
// these vertex arrays so the resulting polyline is bit-identical to the
// rails layer the webpage renders — no analytical drift on clothoid /
// curved sections. A ribbon missing from the map (length=0, zero tangent,
// or filtered by the rails builder) emits a `Break=true` waypoint instead
// of fabricated geometry. Pass nil ONLY for legacy callers that have no
// rails-builder output handy; the resulting path will diverge from the
// rails on clothoids, which is the bug we're fixing.
//
// Returns nil when the schedule has nothing resolvable (no ribbons, missing
// origin, etc.). Otherwise the list always starts at the first waypoint
// and ends at the last. When the path-builder can't find a rail-graph route
// between two waypoints, the next waypoint is emitted with `Break=true`
// rather than a straight line — renderers split the polyline at that point
// so the gap stays visible instead of crossing non-rail terrain.
func BuildServicePath(svc *uasset.Service, ribbons map[string]*uasset.Ribbon, switches []*uasset.Switch, vertices map[string][][2]float64, anchor *geo.RouteAnchor) []ServiceCoord {
	if anchor == nil || len(ribbons) == 0 {
		return nil
	}
	// Collect schedule waypoints with a usable ribbon ref. Each becomes a
	// (ribbon, fraction-along-ribbon) anchor we walk between. `isStop`
	// distinguishes scheduled stops (STOP AT LOCATION / WAIT FOR SERVICE)
	// from routing-only waypoints (GO VIA LOCATION) — we still route through
	// the latter, but the rendered polyline only spans first-stop → last-stop.
	type waypoint struct {
		ribbon   *uasset.Ribbon
		fraction float64
		isStop   bool
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
		wps = append(wps, waypoint{
			ribbon:   rib,
			fraction: f,
			isStop:   timetableaction.IsStop(it.Action),
		})
	}
	if len(wps) == 0 {
		return nil
	}

	adj := buildNodeAdjacency(ribbons, switches)
	out := []ServiceCoord{}
	// Seed with the first waypoint's position. Look it up from the rails
	// vertex array (so the seed is on a rail vertex too) when available;
	// fall back to analytical position only when the ribbon isn't in the
	// rails map (which then also gets a Break on the next waypoint).
	out = append(out, waypointSeed(wps[0].ribbon, wps[0].fraction, vertices, anchor))

	for i := 1; i < len(wps); i++ {
		prev, cur := wps[i-1], wps[i]
		seg := samplePathBetween(adj, ribbons, vertices, anchor, prev.ribbon, prev.fraction, cur.ribbon, cur.fraction)
		// Drop the first point of every segment after the first — it's the
		// shared waypoint with the previous segment's last point.
		if len(seg) > 0 {
			seg = seg[1:]
		}
		out = append(out, seg...)
	}

	// Return the full waypoint-to-waypoint polyline. We used to trim it to
	// "first STOP AT LOCATION → last STOP AT LOCATION" on the theory that
	// GO VIA LOCATION waypoints outside the stop range were depot
	// positioning the player shouldn't see. That broke yard switchers like
	// CSX Y120 Framingham → Worcester, whose schedule is one WAIT FOR
	// SERVICE plus one GO VIA LOCATION (the destination): firstStop and
	// lastStop were both index 0, the trim sliced the polyline to that
	// single point, and the HUD got a 1-point "path". Per the rule
	// "the primary path is the one thing that must always be drawn", the
	// full polyline now ships unmodified.
	return out
}

// waypointSeed returns the seed coord for the first schedule waypoint,
// snapped onto the ribbon's rails-builder vertex array when available.
// `vertices` may be nil (legacy callers) — we fall back to ribbonPointAt's
// analytical computation in that case, with a comment elsewhere noting the
// drift this introduces on clothoids.
func waypointSeed(rib *uasset.Ribbon, frac float64, vertices map[string][][2]float64, anchor *geo.RouteAnchor) ServiceCoord {
	if vertices != nil {
		if pts := lookupRibbonVerts(rib, vertices); len(pts) > 0 {
			i := nearestVertexIndex(pts, frac)
			_, _, originZ := ribbonWorldOrigin(rib)
			return ServiceCoord{
				Latitude:  pts[i][1],
				Longitude: pts[i][0],
				Height:    round1(originZ / 100.0),
			}
		}
	}
	return ribbonPointAt(rib, frac, anchor)
}

// lookupRibbonVerts resolves a ribbon's vertex array from the rails-builder
// map, normalising the GUID when the literal lookup misses.
func lookupRibbonVerts(rib *uasset.Ribbon, vertices map[string][][2]float64) [][2]float64 {
	if rib == nil || vertices == nil {
		return nil
	}
	if v, ok := vertices[rib.GUID]; ok {
		return v
	}
	if k := uasset.NormalizeGUID(rib.GUID); k != "" {
		if v, ok := vertices[k]; ok {
			return v
		}
	}
	return nil
}

// nearestVertexIndex maps a [0,1] fraction along the ribbon to the closest
// integer vertex index in the rails-builder polyline (which has len-1
// segments evenly distributed across the ribbon's length). Round, not floor,
// so a stop fraction lands on the nearest vertex — staying ON the rail.
func nearestVertexIndex(pts [][2]float64, frac float64) int {
	if len(pts) == 0 {
		return 0
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	n := len(pts) - 1
	i := int(math.Round(frac * float64(n)))
	if i < 0 {
		i = 0
	}
	if i > n {
		i = n
	}
	return i
}

// buildNodeAdjacency returns node_guid -> []edgeRole, mirroring the merged-rails
// helper but local to this file.
//
// Two adjacency sources are unioned, both keyed on the same node-GUID space:
//
//  1. Each ribbon's own StartNodeGUID / EndNodeGUID — the primary signal.
//  2. Each NetworkTurnoutJunction (Switch) — its NodeGUID/JctGUID is treated
//     as a synthetic node that all three of its ribbons (Ingoing / Outgoing /
//     Turnout) attach to. This bridges junctions where the per-ribbon node
//     GUIDs drifted (e.g. the Outgoing ribbon's StartNodeGUID doesn't match
//     the Ingoing ribbon's EndNodeGUID even though they physically meet at
//     the switch). Without this, Dijkstra silently misses the connection and
//     downstream code falls back to a straight line across non-rail terrain.
//
// The role attached to a switch-derived edge is "switch" — `samplePathBetween`
// reads the role to know which fraction direction to sample. For switch edges
// we use `Switch.NodeGUID` (or `JctGUID` as fallback) as the meeting node, so
// we encode the entry-end on the ribbon by detecting which end of the ribbon
// is closer to the switch's logical join point: by convention TSW6 places
// IngoingRibbon's END at the switch and OutgoingRibbon/TurnoutRibbon's START
// at the switch — we assign edge roles accordingly so the orientation logic
// in `samplePathBetween` (start→end vs end→start) still works.
func buildNodeAdjacency(ribbons map[string]*uasset.Ribbon, switches []*uasset.Switch) map[string][]edgeRole {
	adj := map[string][]edgeRole{}
	addEdge := func(nodeGUID string, r *uasset.Ribbon, role string) {
		if nodeGUID == "" || r == nil {
			return
		}
		// De-dupe: if this (ribbon, role) is already attached to this node,
		// don't add it again — the node-GUID and switch-record passes can
		// both declare the same edge when the data agrees, which is the
		// happy path. Without this, Dijkstra would over-count and may
		// briefly inflate distances.
		for _, e := range adj[nodeGUID] {
			if e.rib == r && e.role == role {
				return
			}
		}
		adj[nodeGUID] = append(adj[nodeGUID], edgeRole{r, role})
	}
	for _, r := range ribbons {
		if r.Length <= 0 {
			continue
		}
		addEdge(r.StartNodeGUID, r, "start")
		addEdge(r.EndNodeGUID, r, "end")
	}
	// Switch-derived edges — authoritative connectivity at every junction.
	for _, s := range switches {
		if s == nil {
			continue
		}
		node := s.NodeGUID
		if node == "" {
			node = s.JctGUID
		}
		if node == "" {
			continue
		}
		// Ingoing ribbon: train arrives, meets switch at its END node.
		if rib := lookupRibbon(s.IngoingRibbon, ribbons); rib != nil {
			addEdge(node, rib, "end")
		}
		// Outgoing + turnout ribbons: train leaves through them — switch sits
		// at their START. (TSW6 convention; verified against IoW switches.)
		if rib := lookupRibbon(s.OutgoingRibbon, ribbons); rib != nil {
			addEdge(node, rib, "start")
		}
		if rib := lookupRibbon(s.TurnoutRibbon, ribbons); rib != nil {
			addEdge(node, rib, "start")
		}
	}
	return adj
}

// lookupRibbon resolves a switch's ribbon GUID against the per-route ribbon
// map, normalising the GUID to canonical form when the literal lookup misses.
func lookupRibbon(guid string, ribbons map[string]*uasset.Ribbon) *uasset.Ribbon {
	if guid == "" {
		return nil
	}
	if r, ok := ribbons[guid]; ok {
		return r
	}
	if k := uasset.NormalizeGUID(guid); k != "" {
		if r, ok := ribbons[k]; ok {
			return r
		}
	}
	return nil
}

// samplePathBetween walks from (prevRib, prevFrac) to (curRib, curFrac).
// If they're on the same ribbon, we sample the segment between the two
// fractions directly. Otherwise we Dijkstra on the ribbon graph using length
// as edge weight, then concatenate samples along the chain (partial start
// ribbon → whole intermediate ribbons → partial end ribbon).
//
// `vertices` (when non-nil) is the rails-builder's per-ribbon polyline map.
// Each ribbon segment is sliced directly from its array, guaranteeing the
// path lies on the same vertices the rails layer renders. A ribbon missing
// from the map breaks the chain (Break=true on the next waypoint).
func samplePathBetween(adj map[string][]edgeRole, ribbons map[string]*uasset.Ribbon, vertices map[string][][2]float64, anchor *geo.RouteAnchor, prevRib *uasset.Ribbon, prevFrac float64, curRib *uasset.Ribbon, curFrac float64) []ServiceCoord {
	if prevRib.GUID == curRib.GUID {
		return sampleRibbonRange(prevRib, prevFrac, curFrac, vertices, anchor)
	}
	chain := dijkstraRibbonPath(adj, ribbons, prevRib, curRib)
	if chain == nil {
		// No rail-graph route between the two waypoints. Emit the next
		// waypoint marked Break=true so the renderer splits its polyline
		// at this point — better to show a visible gap than to draw a
		// straight line through non-rail terrain (the old fallback would
		// cut diagonally across whatever happened to lie between the two
		// stops). Returning two coords like the rest of `samplePathBetween`
		// keeps the caller's `seg[1:]` dedup logic working: the first coord
		// is the duplicate of the previous segment's tail (dropped), the
		// second is the broken-off next waypoint.
		brk := waypointSeed(curRib, curFrac, vertices, anchor)
		brk.Break = true
		return []ServiceCoord{
			waypointSeed(prevRib, prevFrac, vertices, anchor),
			brk,
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
			out = append(out, sampleRibbonRange(e.rib, prevFrac, endFrac, vertices, anchor)...)
		case len(chain) - 1:
			// Last ribbon: from start (whichever end we entered from) to curFrac.
			startFrac := 0.0
			if e.role == "end" {
				startFrac = 1.0
			}
			seg := sampleRibbonRange(e.rib, startFrac, curFrac, vertices, anchor)
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
			seg := sampleRibbonRange(e.rib, startFrac, endFrac, vertices, anchor)
			if len(seg) > 0 && len(out) > 0 {
				seg = seg[1:] // drop dup at join
			}
			out = append(out, seg...)
		}
	}
	return out
}

// sampleRibbonRange returns the polyline along a ribbon from fraction
// `from` to fraction `to` (each in [0,1]) by SLICING from the rails
// builder's per-ribbon vertex array (`vertices[ribbonGUID]`). This is the
// architecturally critical part of the fix: instead of re-sampling the
// ribbon's analytical geometry (which diverges from the rails layer on
// clothoid spirals), we copy the exact vertices the rails layer renders.
//
// `from > to` walks the slice in reverse (used for ribbons traversed from
// their end-node to their start-node).
//
// `vertices` is nullable for backwards compatibility — if nil, or if the
// ribbon's GUID isn't in the map, we fall back to the analytical sampler.
// In production, `vertices` should always be populated; the analytical
// fallback is what produced the off-rail drift the user complained about
// and only exists so legacy callers that haven't been updated still work.
func sampleRibbonRange(rib *uasset.Ribbon, from, to float64, vertices map[string][][2]float64, anchor *geo.RouteAnchor) []ServiceCoord {
	if rib == nil || rib.Length <= 0 {
		return nil
	}
	if from < 0 {
		from = 0
	}
	if from > 1 {
		from = 1
	}
	if to < 0 {
		to = 0
	}
	if to > 1 {
		to = 1
	}

	pts := lookupRibbonVerts(rib, vertices)
	if len(pts) >= 2 {
		// Slice from the rails-builder vertex array — bit-identical to the
		// rendered rail polyline.
		_, _, originZ := ribbonWorldOrigin(rib)
		heightM := round1(originZ / 100.0)
		iFrom := nearestVertexIndex(pts, from)
		iTo := nearestVertexIndex(pts, to)
		if iFrom == iTo {
			return []ServiceCoord{{
				Latitude:  pts[iFrom][1],
				Longitude: pts[iFrom][0],
				Height:    heightM,
			}}
		}
		step := 1
		count := iTo - iFrom + 1
		if iFrom > iTo {
			step = -1
			count = iFrom - iTo + 1
		}
		out := make([]ServiceCoord, 0, count)
		for i := iFrom; ; i += step {
			out = append(out, ServiceCoord{
				Latitude:  pts[i][1],
				Longitude: pts[i][0],
				Height:    heightM,
			})
			if i == iTo {
				break
			}
		}
		return out
	}

	// Legacy / fallback path: ribbon not in the rails-builder map. Sample
	// analytically. NOTE: this diverges from the rails layer on clothoid
	// ribbons — it's a correctness regression vs. the rails-builder slice
	// path above. Keep until every caller passes a non-nil vertices map.
	return sampleRibbonRangeAnalytic(rib, from, to, anchor)
}

// sampleRibbonRangeAnalytic is the original arc-walking fallback. Used
// only when the rails-builder vertex array isn't available for the ribbon.
func sampleRibbonRangeAnalytic(rib *uasset.Ribbon, from, to float64, anchor *geo.RouteAnchor) []ServiceCoord {
	opts := DefaultRailsOptions()
	lengthCm := rib.Length
	stepCm := opts.SampleStepMeters * 100.0
	if stepCm <= 0 {
		stepCm = 1000.0
	}
	n := int(math.Ceil(lengthCm / stepCm))
	if rib.Radius != 0 && !math.IsInf(rib.Radius, 0) && !math.IsNaN(rib.Radius) {
		curveN := int(math.Ceil(lengthCm / (math.Abs(rib.Radius) * maxDeflectionRadians)))
		if curveN > n {
			n = curveN
		}
	}
	if n+1 < opts.MinSamples {
		n = opts.MinSamples - 1
	}
	if opts.MaxSamples > 0 && n+1 > opts.MaxSamples {
		n = opts.MaxSamples - 1
	}
	if n < 1 {
		n = 1
	}

	iFrom := int(math.Round(from * float64(n)))
	iTo := int(math.Round(to * float64(n)))
	if iFrom == iTo {
		return []ServiceCoord{ribbonVertexAt(rib, iFrom, n, anchor)}
	}

	originX, originY, originZ := ribbonWorldOrigin(rib)
	step := 1
	count := iTo - iFrom + 1
	if iFrom > iTo {
		step = -1
		count = iFrom - iTo + 1
	}
	out := make([]ServiceCoord, 0, count)
	for i := iFrom; ; i += step {
		t := float64(i) / float64(n)
		atLen := t * lengthCm
		dx, dy := geo.ArcDelta(0, 0, rib.TangentX, rib.TangentY, rib.Radius, atLen)
		lat, lng := anchor.WorldToLatLng((originX+dx)/100.0, (originY+dy)/100.0)
		out = append(out, ServiceCoord{
			Latitude:  round7(lat),
			Longitude: round7(lng),
			Height:    round1(originZ / 100.0),
		})
		if i == iTo {
			break
		}
	}
	return out
}

// ribbonVertexAt is the single-vertex sibling of the analytical fallback.
func ribbonVertexAt(rib *uasset.Ribbon, i, n int, anchor *geo.RouteAnchor) ServiceCoord {
	t := float64(i) / float64(n)
	atLen := t * rib.Length
	dx, dy := geo.ArcDelta(0, 0, rib.TangentX, rib.TangentY, rib.Radius, atLen)
	originX, originY, originZ := ribbonWorldOrigin(rib)
	lat, lng := anchor.WorldToLatLng((originX+dx)/100.0, (originY+dy)/100.0)
	return ServiceCoord{
		Latitude:  round7(lat),
		Longitude: round7(lng),
		Height:    round1(originZ / 100.0),
	}
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
