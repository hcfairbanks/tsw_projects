package output

import (
	"encoding/json"
	"io"
	"sort"

	"hud-go/internal/geo"
	"hud-go/internal/pak/uasset"
)

// railsGeometryMode selects per-ribbon geometry detail when emitting the
// merged rails file.
type railsGeometryMode int

const (
	// railsArc samples each ribbon's curve geometry (smooth, slightly larger).
	railsArc railsGeometryMode = iota
	// railsStraight uses only ribbon endpoints (straight lines between joins).
	railsStraight
)

// edgeRole records which end of a ribbon attaches to a graph node — start or
// end. The ribbon is bidirectional geometry; we use the role to know how to
// orient samples when concatenating into a continuous polyline.
type edgeRole struct {
	rib  *uasset.Ribbon
	role string // "start" or "end"
}

// WriteRailsMergedArc emits the rail network as a single MultiLineString
// feature with arc-sampled per-ribbon geometry. Each sub-line is a maximally
// continuous chain of ribbons (split only at junctions, dead-ends, or real
// disconnects). Renderers draw it as one feature; downstream code never has to
// think about per-ribbon identity.
func WriteRailsMergedArc(w io.Writer, tt *uasset.Timetable, opts RailsGeoJSONOptions) (int, error) {
	return writeRailsMerged(w, tt, opts, railsArc)
}

// WriteRailsMergedStraight is like WriteRailsMergedArc but uses only ribbon
// endpoints (straight lines between joins). Smaller files, no curve detail.
func WriteRailsMergedStraight(w io.Writer, tt *uasset.Timetable) (int, error) {
	return writeRailsMerged(w, tt, RailsGeoJSONOptions{}, railsStraight)
}

func writeRailsMerged(w io.Writer, tt *uasset.Timetable, opts RailsGeoJSONOptions, mode railsGeometryMode) (int, error) {
	if tt == nil {
		return 0, nil
	}
	if tt.OriginLat == 0 && tt.OriginLng == 0 {
		return 0, nil
	}
	ribbons := tt.RouteRibbons
	if len(ribbons) == 0 {
		ribbons = tt.Ribbons
	}
	if len(ribbons) == 0 {
		return 0, nil
	}
	if mode == railsArc {
		if opts.SampleStepMeters <= 0 {
			opts.SampleStepMeters = 10
		}
		if opts.MinSamples < 2 {
			opts.MinSamples = 2
		}
		if opts.MaxSamples < opts.MinSamples {
			opts.MaxSamples = 400
		}
	}

	anchor := geo.NewRouteAnchor(tt.OriginLat, tt.OriginLng)
	paths := buildRibbonPaths(ribbons)

	multi := make([][][2]float64, 0, len(paths))
	for _, p := range paths {
		coords := buildPathCoords(p, anchor, opts, mode)
		if len(coords) >= 2 {
			multi = append(multi, coords)
		}
	}

	geomMode := "arc"
	if mode == railsStraight {
		geomMode = "straight"
	}

	feat := map[string]any{
		"type": "Feature",
		"properties": map[string]any{
			"route":         tt.Route,
			"segment_count": len(multi),
			"ribbon_count":  len(ribbons),
			"geometry_mode": geomMode,
		},
		"geometry": map[string]any{
			"type":        "MultiLineString",
			"coordinates": multi,
		},
	}
	doc := map[string]any{
		"type":     "FeatureCollection",
		"name":     "rails-" + tt.Route,
		"features": []any{feat},
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return 0, err
	}
	return len(multi), nil
}

// buildRibbonPaths walks the ribbon node-graph and returns ordered ribbon
// sequences (each sequence becomes one LineString in the MultiLineString).
// Linear chains of degree-2 nodes are merged; junctions and dead-ends become
// path boundaries. Pure cycles (no junction or dead-end) are emitted as a
// single closed path each.
func buildRibbonPaths(ribbons map[string]*uasset.Ribbon) [][]edgeRole {
	adj := map[string][]edgeRole{}
	ribList := make([]*uasset.Ribbon, 0, len(ribbons))
	for _, r := range ribbons {
		if r.Length <= 0 {
			continue
		}
		ribList = append(ribList, r)
		if r.StartNodeGUID != "" {
			adj[r.StartNodeGUID] = append(adj[r.StartNodeGUID], edgeRole{r, "start"})
		}
		if r.EndNodeGUID != "" {
			adj[r.EndNodeGUID] = append(adj[r.EndNodeGUID], edgeRole{r, "end"})
		}
	}
	sort.Slice(ribList, func(i, j int) bool { return ribList[i].GUID < ribList[j].GUID })

	visited := map[string]bool{}
	var paths [][]edgeRole

	walk := func(startEdge edgeRole) []edgeRole {
		path := []edgeRole{}
		cur := startEdge
		for {
			if visited[cur.rib.GUID] {
				break
			}
			visited[cur.rib.GUID] = true
			path = append(path, cur)
			var opposite string
			if cur.role == "start" {
				opposite = cur.rib.EndNodeGUID
			} else {
				opposite = cur.rib.StartNodeGUID
			}
			if opposite == "" {
				break
			}
			edges := adj[opposite]
			if len(edges) != 2 {
				break // junction or dead-end
			}
			// At the shared node, the current ribbon's role THERE is the
			// opposite of cur.role (we're walking out of it).
			curRoleAtNode := "end"
			if cur.role == "end" {
				curRoleAtNode = "start"
			}
			var nextEdge edgeRole
			found := false
			for _, e := range edges {
				if e.rib.GUID == cur.rib.GUID {
					continue
				}
				// Only continue the chain when the next ribbon's role at this
				// node is the OPPOSITE of ours — i.e. one ribbon ends here
				// and the next one starts here. If both ribbons end (or both
				// start) at this node, they're converging, not chaining;
				// merging them would draw a U-turn / X-crossing.
				if e.role == curRoleAtNode {
					continue
				}
				nextEdge = e
				found = true
				break
			}
			if !found || visited[nextEdge.rib.GUID] {
				break
			}
			cur = nextEdge
		}
		return path
	}

	// Stable starting-node order: junctions and dead-ends sorted by GUID
	junctionNodes := make([]string, 0)
	for n, edges := range adj {
		if len(edges) != 2 {
			junctionNodes = append(junctionNodes, n)
		}
	}
	sort.Strings(junctionNodes)

	for _, n := range junctionNodes {
		for _, e := range adj[n] {
			if visited[e.rib.GUID] {
				continue
			}
			p := walk(e)
			if len(p) > 0 {
				paths = append(paths, p)
			}
		}
	}
	// Isolated cycles or sub-graphs without any junction/dead-end
	for _, r := range ribList {
		if visited[r.GUID] {
			continue
		}
		p := walk(edgeRole{r, "start"})
		if len(p) > 0 {
			paths = append(paths, p)
		}
	}
	return paths
}

// buildPathCoords concatenates ribbon coords in order, orienting each ribbon
// along the path direction. Every ribbon after the first drops its leading
// sample point because that point IS the shared NetworkNode the previous
// ribbon already ended at — even though their world positions can differ by
// up to ~2 m (source-data precision), they represent the same logical join.
// Snapping to one of them avoids drawing a tiny zig-zag at every node.
func buildPathCoords(path []edgeRole, anchor *geo.RouteAnchor, opts RailsGeoJSONOptions, mode railsGeometryMode) [][2]float64 {
	out := make([][2]float64, 0)
	for i, e := range path {
		var rc [][2]float64
		switch mode {
		case railsArc:
			rc = sampleRibbonLatLng(e.rib, anchor, opts)
		case railsStraight:
			s, end, ok := ribbonStartEndLatLng(e.rib, anchor)
			if !ok {
				continue
			}
			rc = [][2]float64{s, end}
		}
		if len(rc) < 2 {
			continue
		}
		if e.role == "end" {
			for a, b := 0, len(rc)-1; a < b; a, b = a+1, b-1 {
				rc[a], rc[b] = rc[b], rc[a]
			}
		}
		if i > 0 && len(out) > 0 {
			// Drop the leading point — it's the shared node, already in `out`.
			rc = rc[1:]
		}
		out = append(out, rc...)
	}
	return out
}

func ribbonStartEndLatLng(rib *uasset.Ribbon, anchor *geo.RouteAnchor) (start, end [2]float64, ok bool) {
	if rib == nil || rib.Length <= 0 {
		return [2]float64{}, [2]float64{}, false
	}
	originX, originY, _ := ribbonWorldOrigin(rib)
	sLat, sLng := anchor.WorldToLatLng(originX/100.0, originY/100.0)
	dx, dy := geo.ArcDelta(0, 0, rib.TangentX, rib.TangentY, rib.Radius, rib.Length)
	eLat, eLng := anchor.WorldToLatLng((originX+dx)/100.0, (originY+dy)/100.0)
	return [2]float64{round7(sLng), round7(sLat)}, [2]float64{round7(eLng), round7(eLat)}, true
}

// RibbonMeta is the lightweight per-ribbon record written to <Route>_ribbons.json.
// No arc samples — just identity, endpoints, and graph links.
type RibbonMeta struct {
	StartLat      float64 `json:"start_lat"`
	StartLng      float64 `json:"start_lng"`
	EndLat        float64 `json:"end_lat"`
	EndLng        float64 `json:"end_lng"`
	StartNodeGUID string  `json:"start_node_guid,omitempty"`
	EndNodeGUID   string  `json:"end_node_guid,omitempty"`
	LengthM       float64 `json:"length_m"`
	Tile          string  `json:"tile,omitempty"`
}

// WriteRibbonsMeta writes a JSON map of {ribbon_guid: RibbonMeta} for the
// route's own ribbons. Used by graph algorithms (shortest-path, etc.) and by
// runtime code that needs to map a RibbonGUID to its endpoints without the
// arc-sampled rendering overhead.
func WriteRibbonsMeta(w io.Writer, tt *uasset.Timetable) (int, error) {
	if tt == nil {
		return 0, nil
	}
	if tt.OriginLat == 0 && tt.OriginLng == 0 {
		return 0, nil
	}
	ribbons := tt.RouteRibbons
	if len(ribbons) == 0 {
		ribbons = tt.Ribbons
	}
	if len(ribbons) == 0 {
		return 0, nil
	}
	anchor := geo.NewRouteAnchor(tt.OriginLat, tt.OriginLng)

	keys := make([]string, 0, len(ribbons))
	for k := range ribbons {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]RibbonMeta, len(ribbons))
	for _, k := range keys {
		r := ribbons[k]
		s, e, ok := ribbonStartEndLatLng(r, anchor)
		if !ok {
			continue
		}
		out[r.GUID] = RibbonMeta{
			StartLat:      s[1],
			StartLng:      s[0],
			EndLat:        e[1],
			EndLng:        e[0],
			StartNodeGUID: r.StartNodeGUID,
			EndNodeGUID:   r.EndNodeGUID,
			LengthM:       round1(r.Length / 100.0),
			Tile:          r.TileName,
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return 0, err
	}
	return len(out), nil
}
