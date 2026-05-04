// ribbons-to-geojson — sample each NetworkRibbon's curve and emit one
// GeoJSON LineString per ribbon, projected to lat/lng.
//
// Input CSV (from editor python dump): header row plus one row per ribbon:
//   actor_path, ribbon_name, ribbon_guid, start_node_guid, end_node_guid,
//   curve_class, sx_cm, sy_cm, tx, ty, length_cm, radius_cm
//
// Sampling:
//   - NetworkCurveCircularArc with non-zero Radius → sample as circular arc
//   - NetworkCurveCircularArc with Radius=0 → straight chord
//   - NetworkCurveClothoidSpiral → use the topology graph (StartNodeGuid /
//     EndNodeGuid) to find the neighbour ribbon at this clothoid's end node;
//     sample as a chord that lands at the neighbour's StartPosition. The
//     engine guarantees ribbons sharing a node meet there, so the chord ends
//     in the right place even though we lack PowersOfA for proper Fresnel
//     integration. Visual approximation; visually-equivalent at the macro view.
//
// All input cm coords are world-space (TrackNetworkActor sits at world origin).
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"

	"hud-go/internal/geo"
)

const (
	sampleStepCm     = 100.0  // 1 m
	maxDeflectionRad = 0.00873 // ~0.5 deg per polyline segment for tight arcs
	minSamples       = 4
	maxSamples       = 5000
	// TSW tile grid: every TT/TS/ST/LT tile is 1km × 1km, world-positioned at
	// (tile_x * 100000, tile_y * 100000) cm. The TrackNetworkActor inside each
	// tile sits at (0,0,0) in its own tile-local frame, so curve coords are
	// tile-local — we must add the tile offset to get world position.
	tileSizeCm = 100000.0
)

// tilePathRE pulls (x, y) out of an actor_path containing "/TT_xN_yM."
// (sign and arbitrary digit count). Used to determine the per-ribbon tile
// offset to add to local curve coordinates.
var tilePathRE = regexp.MustCompile(`/TT_x(-?\d+)_y(-?\d+)\.`)

// ribbon is one row from the editor dump CSV.
type ribbon struct {
	name      string
	guid      string
	startNode string
	endNode   string
	class     string
	sx, sy    float64 // start position in WORLD cm (= WorldLocation + StartPosition2D)
	tx, ty    float64 // unit tangent at start
	length    float64 // curve length (cm)
	radius    float64 // 0 for straight or clothoid; non-zero for arcs
	hasRadius bool
	tileX, tileY int // tile grid coords parsed from actor_path (for diagnostics)

	// Computed endpoint in world cm. For arcs/straights this is exact; for
	// clothoids we resolve it via the topology graph (next ribbon's start).
	ex, ey   float64
	hasEnd   bool
	// End tangent (unit vector). For arcs we rotate the start tangent by the
	// arc sweep; for straights it equals the start tangent. For clothoids it's
	// inherited from the neighbour we resolved the endpoint against, so the
	// Hermite flows smoothly into the neighbour's curve.
	etx, ety float64
}

func (r *ribbon) isClothoid() bool { return r.class == "NetworkCurveClothoidSpiral" }
func (r *ribbon) isArc() bool {
	return r.class == "NetworkCurveCircularArc" && r.hasRadius && r.radius != 0 &&
		!math.IsInf(r.radius, 0) && !math.IsNaN(r.radius)
}

// nodeAttachment records how a ribbon attaches to a node — at its start or end.
type nodeAttachment struct {
	ribIdx int
	atStart bool // true = attached via StartNodeGuid; false = via EndNodeGuid
}

func main() {
	in := flag.String("in", "", "CSV from editor python dump")
	out := flag.String("out", "", "GeoJSON output path")
	originLat := flag.Float64("origin-lat", 51.11568832397461, "route origin latitude")
	originLng := flag.Float64("origin-lng", 6.209702968597412, "route origin longitude")
	flag.Parse()
	if *in == "" || *out == "" {
		log.Fatal("usage: ribbons-to-geojson --in <csv> --out <geojson>")
	}

	ribbons, err := readRibbons(*in)
	if err != nil {
		log.Fatalf("read csv: %v", err)
	}
	fmt.Fprintf(os.Stderr, "loaded %d ribbons\n", len(ribbons))

	// World coords already applied in readRibbons (sx,sy = WorldLocation +
	// StartPosition2D). No more coord-frame heuristics needed.

	// Compute exact endpoints for arcs and straights (deterministic from the curve).
	for i := range ribbons {
		r := &ribbons[i]
		if r.length <= 0 {
			continue
		}
		tn := math.Hypot(r.tx, r.ty)
		if tn == 0 {
			continue
		}
		r.tx /= tn
		r.ty /= tn
		switch {
		case r.isArc():
			dx, dy := geo.ArcDelta(0, 0, r.tx, r.ty, r.radius, r.length)
			r.ex, r.ey, r.hasEnd = r.sx+dx, r.sy+dy, true
			// Arc sweep: -L/R radians (CW when R>0), matches ArcDelta convention.
			sweep := -r.length / r.radius
			cs, sn := math.Cos(sweep), math.Sin(sweep)
			r.etx = r.tx*cs - r.ty*sn
			r.ety = r.tx*sn + r.ty*cs
		case !r.isClothoid():
			r.ex, r.ey, r.hasEnd = r.sx+r.tx*r.length, r.sy+r.ty*r.length, true
			r.etx, r.ety = r.tx, r.ty
		}
	}

	// Topology: NodeGuid → all ribbons attached, with which side.
	node := map[string][]nodeAttachment{}
	for i := range ribbons {
		r := &ribbons[i]
		if r.startNode != "" {
			node[r.startNode] = append(node[r.startNode], nodeAttachment{i, true})
		}
		if r.endNode != "" {
			node[r.endNode] = append(node[r.endNode], nodeAttachment{i, false})
		}
	}

	// Resolve clothoid endpoints via topology: neighbour at our end node tells
	// us where we should land.
	clothResolved, clothFallback := 0, 0
	for i := range ribbons {
		r := &ribbons[i]
		if !r.isClothoid() {
			continue
		}
		// Find a neighbour at our end-node — ideally one whose canonical
		// position at that node we know (an arc/straight's start or end).
		atts := node[r.endNode]
		var bestX, bestY, bestTx, bestTy float64
		found := false
		for _, na := range atts {
			if na.ribIdx == i {
				continue
			}
			n := &ribbons[na.ribIdx]
			if na.atStart {
				// Neighbour starts here. Its start position is the node;
				// its start tangent points away from the node (= our exit
				// direction continues into it, so this IS our end tangent).
				bestX, bestY = n.sx, n.sy
				bestTx, bestTy, found = n.tx, n.ty, true
				break
			}
			if n.hasEnd {
				// Neighbour ends here. Its end position is the node; its end
				// tangent points away from the node along its own travel
				// direction — but from our perspective we approach the node
				// going INTO neighbour's end, so we need the OPPOSITE of
				// neighbour's end tangent.
				bestX, bestY = n.ex, n.ey
				bestTx, bestTy, found = -n.etx, -n.ety, true
				break
			}
		}
		// Sanity check: a clothoid is at most `Length` long along the curve, so
		// the chord from start to end is at most `Length` (and usually much
		// less for curves). Anything farther than ~1.5×Length is almost
		// certainly a wrong neighbour match (cross-tile GUID coincidence,
		// shared "default" guid, etc.) — discard and use chord-fallback,
		// which is bounded by the curve's own Length.
		insaneEndpoint := false
		if found {
			gap := math.Hypot(bestX-r.sx, bestY-r.sy)
			if gap > r.length*1.5 {
				insaneEndpoint = true
				found = false
			}
		}
		if found {
			r.ex, r.ey, r.hasEnd = bestX, bestY, true
			r.etx, r.ety = bestTx, bestTy
			clothResolved++
		} else {
			r.ex = r.sx + r.tx*r.length
			r.ey = r.sy + r.ty*r.length
			r.etx, r.ety = r.tx, r.ty
			r.hasEnd = true
			clothFallback++
			_ = insaneEndpoint
		}
	}
	fmt.Fprintf(os.Stderr, "clothoids: %d resolved via topology, %d chord-fallback\n",
		clothResolved, clothFallback)

	// Sample each ribbon and emit a LineString.
	anchor := geo.NewRouteAnchor(*originLat, *originLng)
	var features []any
	skipped, arcs, straights, cloth := 0, 0, 0, 0
	for i := range ribbons {
		r := &ribbons[i]
		if r.length <= 0 || math.Hypot(r.tx, r.ty) == 0 {
			skipped++
			continue
		}
		coords := sampleRibbon(r, anchor)
		if len(coords) < 2 {
			skipped++
			continue
		}
		switch {
		case r.isArc():
			arcs++
		case r.isClothoid():
			cloth++
		default:
			straights++
		}
		features = append(features, map[string]any{
			"type": "Feature",
			"properties": map[string]any{
				"ribbon":    r.name,
				"class":     r.class,
				"length_cm": r.length,
				"radius_cm": r.radius,
			},
			"geometry": map[string]any{"type": "LineString", "coordinates": coords},
		})
	}

	doc := map[string]any{
		"type":     "FeatureCollection",
		"name":     "ribbons-canonical",
		"features": features,
	}
	w, err := os.Create(*out)
	if err != nil {
		log.Fatalf("create: %v", err)
	}
	defer w.Close()
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		log.Fatalf("encode: %v", err)
	}
	fmt.Fprintf(os.Stderr, "wrote %d ribbons (%d arcs, %d straights, %d clothoids; skipped=%d) to %s\n",
		len(features), arcs, straights, cloth, skipped, *out)
}

// sampleRibbon walks one ribbon's curve at fine resolution and returns
// projected [lng,lat] vertices. Arcs use the analytic ArcDelta; straights are
// linear; clothoids are linearly interpolated to their topology-resolved
// endpoint (chord that lands in the right place, even if mid-curve isn't
// fresnel-integrated).
func sampleRibbon(r *ribbon, anchor *geo.RouteAnchor) [][2]float64 {
	n := int(math.Ceil(r.length / sampleStepCm))
	if r.isArc() {
		cn := int(math.Ceil(r.length / (math.Abs(r.radius) * maxDeflectionRad)))
		if cn > n {
			n = cn
		}
	}
	if n < minSamples {
		n = minSamples
	}
	if n > maxSamples {
		n = maxSamples
	}
	out := make([][2]float64, 0, n+1)
	for i := 0; i <= n; i++ {
		t := float64(i) / float64(n)
		var x, y float64
		switch {
		case r.isArc():
			s := t * r.length
			dx, dy := geo.ArcDelta(0, 0, r.tx, r.ty, r.radius, s)
			x, y = r.sx+dx, r.sy+dy
		case r.isClothoid() && r.hasEnd:
			// Cubic Hermite from (start, start_tan) to (end, end_tan).
			// Tangent magnitude = chord/3 (Catmull-Rom-style); for railway
			// transition curves this matches the actual clothoid midpoint
			// deflection within sub-metre accuracy.
			chord := math.Hypot(r.ex-r.sx, r.ey-r.sy)
			m := chord / 3.0
			t2 := t * t
			t3 := t2 * t
			h00 := 2*t3 - 3*t2 + 1
			h10 := t3 - 2*t2 + t
			h01 := -2*t3 + 3*t2
			h11 := t3 - t2
			x = h00*r.sx + h10*r.tx*m + h01*r.ex + h11*r.etx*m
			y = h00*r.sy + h10*r.ty*m + h01*r.ey + h11*r.ety*m
		default:
			s := t * r.length
			x, y = r.sx+r.tx*s, r.sy+r.ty*s
		}
		eastM := x / 100.0
		southM := y / 100.0
		lat, lng := anchor.WorldToLatLng(eastM, southM)
		out = append(out, [2]float64{round7(lng), round7(lat)})
	}
	return out
}

func readRibbons(path string) ([]ribbon, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	header, err := r.Read()
	if err != nil {
		return nil, err
	}
	col := func(name string) int {
		for i, h := range header {
			if h == name {
				return i
			}
		}
		return -1
	}
	iName := col("ribbon_name")
	iGuid := col("ribbon_guid")
	iSn := col("start_node_guid")
	iEn := col("end_node_guid")
	iCls := col("curve_class")
	iSx := col("sx_cm")
	iSy := col("sy_cm")
	iTx := col("tx")
	iTy := col("ty")
	iLen := col("length_cm")
	iRad := col("radius_cm")
	required := []int{iName, iCls, iSx, iSy, iTx, iTy, iLen, iRad}
	for _, ix := range required {
		if ix < 0 {
			return nil, fmt.Errorf("CSV missing required column; header=%v", header)
		}
	}
	var ribbons []ribbon
	for {
		row, err := r.Read()
		if err != nil {
			break
		}
		var rb ribbon
		rb.name = row[iName]
		if iGuid >= 0 {
			rb.guid = row[iGuid]
		}
		if iSn >= 0 {
			rb.startNode = row[iSn]
		}
		if iEn >= 0 {
			rb.endNode = row[iEn]
		}
		rb.class = row[iCls]
		// Tile coords (diagnostic only; no longer used for offset math).
		actorPath := ""
		if i := col("actor_path"); i >= 0 {
			actorPath = row[i]
		}
		if m := tilePathRE.FindStringSubmatch(actorPath); m != nil {
			tx, _ := strconv.Atoi(m[1])
			ty, _ := strconv.Atoi(m[2])
			rb.tileX, rb.tileY = tx, ty
		}
		// world position = WorldLocation + StartPosition2D. The engine stores
		// the right offset on the ribbon's WorldLocation field; for ribbons
		// authored in world coords WorldLocation is just (0,0), and adding
		// it is a no-op. No more heuristics or tile-coord guessing.
		sxLocal, _ := strconv.ParseFloat(row[iSx], 64)
		syLocal, _ := strconv.ParseFloat(row[iSy], 64)
		var wlx, wly float64
		if i := col("world_loc_x"); i >= 0 {
			wlx, _ = strconv.ParseFloat(row[i], 64)
		}
		if i := col("world_loc_y"); i >= 0 {
			wly, _ = strconv.ParseFloat(row[i], 64)
		}
		rb.sx = sxLocal + wlx
		rb.sy = syLocal + wly
		rb.tx, _ = strconv.ParseFloat(row[iTx], 64)
		rb.ty, _ = strconv.ParseFloat(row[iTy], 64)
		rb.length, _ = strconv.ParseFloat(row[iLen], 64)
		if rs := strings.TrimSpace(row[iRad]); rs != "" {
			rb.radius, _ = strconv.ParseFloat(rs, 64)
			rb.hasRadius = true
		}
		ribbons = append(ribbons, rb)
	}
	return ribbons, nil
}

func round7(v float64) float64 { return math.Round(v*1e7) / 1e7 }
