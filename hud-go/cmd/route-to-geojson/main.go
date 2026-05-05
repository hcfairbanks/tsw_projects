// route-to-geojson — end-to-end map producer. Given a route's unpacked
// pak workdir, produces ONE GeoJSON containing rail centerlines as a
// MultiLineString plus every feature (platforms, signals, switches,
// car-stop-signs, route-markers, track-features) as Point/LineString
// Features.
//
// Pipeline:
//   1. Walk every TT_*.umap with the binary parser
//   2. Collect CookedRibbons + CookedFeatures (platforms, signals, switches,
//      car-stop-signs, route-markers)
//   3. Adapt CookedRibbons → uasset.Ribbon (with CachedStartPosition set
//      directly from world cm) so the existing internal/output/ writers
//      can render the features unchanged
//   4. Sample each ribbon's curve into a LineString (re-uses the same
//      arc + clothoid-via-topology logic as ribbons-to-geojson)
//   5. Merge into one FeatureCollection
//
// No editor required. Uses only the cooked .umap binaries via the in-
// process Go uasset parser.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"hud-go/internal/geo"
	"hud-go/internal/output"
	"hud-go/internal/pak/uasset"
)

// Tile filenames are `<prefix>_x<int>_y<int>` (TT_x10_y-3 etc). Tile world
// origin = (tile_x * 100000, tile_y * 100000) cm — same as everywhere else
// in the pipeline.
var tileXYRE = regexp.MustCompile(`_x(-?\d+)_y(-?\d+)$`)

func parseTileXY(base string) (x, y int, ok bool) {
	m := tileXYRE.FindStringSubmatch(base)
	if m == nil {
		return 0, 0, false
	}
	xv, e1 := strconv.Atoi(m[1])
	yv, e2 := strconv.Atoi(m[2])
	if e1 != nil || e2 != nil {
		return 0, 0, false
	}
	return xv, yv, true
}

const (
	sampleStepCm     = 100.0
	maxDeflectionRad = 0.00873
	minSamples       = 4
	maxSamples       = 5000
)

func main() {
	workdir := flag.String("workdir", "", "directory of unpacked tile umaps (recursive)")
	out := flag.String("out", "", "output GeoJSON path")
	originLat := flag.Float64("origin-lat", 0, "route origin latitude (auto-detected if 0)")
	originLng := flag.Float64("origin-lng", 0, "route origin longitude (auto-detected if 0)")
	routeName := flag.String("name", "route", "route name in the GeoJSON properties")
	flag.Parse()
	if *workdir == "" || *out == "" {
		log.Fatal("usage: route-to-geojson --workdir <dir> --out <geojson> [--origin-lat ... --origin-lng ...] [--name <name>]")
	}

	// Auto-detect origin if not given.
	if *originLat == 0 || *originLng == 0 {
		la, ln, ok := autoFindOrigin(*workdir)
		if !ok {
			log.Fatal("origin not provided and auto-detect failed; pass --origin-lat / --origin-lng")
		}
		*originLat, *originLng = la, ln
		fmt.Fprintf(os.Stderr, "[route-to-geojson] auto-detected origin: (%.10f, %.10f)\n", la, ln)
	}

	// Walk all TT_*.umap files.
	t0 := time.Now()
	var tilePaths []string
	_ = filepath.WalkDir(*workdir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		base := d.Name()
		if !strings.HasSuffix(strings.ToLower(base), ".umap") {
			return nil
		}
		if !strings.HasPrefix(base, "TT_") {
			return nil
		}
		tilePaths = append(tilePaths, p)
		return nil
	})
	fmt.Fprintf(os.Stderr, "[route-to-geojson] %d TT tiles\n", len(tilePaths))

	allRibbons := []uasset.CookedRibbon{}
	allPlatforms := []*uasset.LinkedPlatform{}
	allSignals := []*uasset.Signal{}
	allSwitches := []*uasset.Switch{}
	allStops := []*uasset.CarStopSign{}
	allMarkers := []*uasset.RouteMarker{}
	for _, p := range tilePaths {
		tileName := strings.TrimSuffix(filepath.Base(p), ".umap")
		ribs, err := uasset.ParseCookedRibbonsFromUmap(p, tileName)
		if err == nil {
			allRibbons = append(allRibbons, ribs...)
		}
		fts, err := uasset.ParseCookedFeaturesFromUmap(p, tileName)
		if err == nil && fts != nil {
			allPlatforms = append(allPlatforms, fts.Platforms...)
			allSignals = append(allSignals, fts.Signals...)
			allSwitches = append(allSwitches, fts.Switches...)
			allStops = append(allStops, fts.CarStopSigns...)
			allMarkers = append(allMarkers, fts.RouteMarkers...)
		}
	}
	fmt.Fprintf(os.Stderr, "[route-to-geojson] parsed in %s: %d ribbons, %d platforms, %d signals, %d switches, %d car-stop-signs, %d route-markers\n",
		time.Since(t0).Round(time.Millisecond),
		len(allRibbons), len(allPlatforms), len(allSignals), len(allSwitches), len(allStops), len(allMarkers))

	// Collectables live on actor sub-exports under any tile flavour (ST_, TT_,
	// occasionally PD_ etc.) — walk every .umap. Tile-local cm is lifted to
	// world cm via the tile's (x*100000, y*100000) origin.
	tCol := time.Now()
	type worldCollectable struct {
		c       *uasset.CookedCollectable
		worldX  float64
		worldY  float64
	}
	var allCollectables []worldCollectable
	var allUmaps []string
	_ = filepath.WalkDir(*workdir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(d.Name()), ".umap") {
			allUmaps = append(allUmaps, p)
		}
		return nil
	})
	for _, p := range allUmaps {
		base := strings.TrimSuffix(filepath.Base(p), ".umap")
		tx, ty, ok := parseTileXY(base)
		if !ok {
			continue
		}
		cs, err := uasset.ParseCookedCollectablesFromUmap(p, base)
		if err != nil || len(cs) == 0 {
			continue
		}
		ox, oy := float64(tx)*100000.0, float64(ty)*100000.0
		for _, c := range cs {
			allCollectables = append(allCollectables, worldCollectable{
				c:      c,
				worldX: ox + c.X,
				worldY: oy + c.Y,
			})
		}
	}
	fmt.Fprintf(os.Stderr, "[route-to-geojson] collectables in %s: %d across %d umaps\n",
		time.Since(tCol).Round(time.Millisecond), len(allCollectables), len(allUmaps))

	// Adapt CookedRibbons → uasset.Ribbon for the existing GeoJSON writers.
	// We populate CachedStart{X,Y} (world cm) so resolveRibbonOffset's
	// ribbonWorldOrigin returns the correct absolute world cm.
	ribbonsByGUID := map[string]*uasset.Ribbon{}
	ribbonList := make([]*uasset.Ribbon, 0, len(allRibbons))
	for i := range allRibbons {
		cr := &allRibbons[i]
		r := &uasset.Ribbon{
			GUID:           cr.RibbonGUID,
			TileName:       cr.TileName,
			StartNodeGUID:  cr.StartNodeGUID,
			EndNodeGUID:    cr.EndNodeGUID,
			TangentX:       cr.TangentX,
			TangentY:       cr.TangentY,
			Radius:         cr.Radius,
			Length:         cr.Length,
			CachedStartX:   cr.StartX,
			CachedStartY:   cr.StartY,
			HasCachedStart: true,
			IsClothoid:     cr.CurveClass == "NetworkCurveClothoidSpiral",
		}
		ribbonList = append(ribbonList, r)
		ribbonsByGUID[uasset.NormalizeGUID(r.GUID)] = r
	}

	tt := &uasset.Timetable{
		Route:           *routeName,
		OriginLat:       *originLat,
		OriginLng:       *originLng,
		Ribbons:         ribbonsByGUID,
		RouteRibbons:    ribbonsByGUID,
		LinkedPlatforms: allPlatforms,
		Signals:         allSignals,
		Switches:        allSwitches,
		CarStopSigns:    allStops,
		RouteMarkers:    allMarkers,
	}

	// Build the rails MultiLineString — sample each ribbon's curve. Clothoids
	// resolve their endpoints via the topology graph (next ribbon's start)
	// just like ribbons-to-geojson does.
	anchor := geo.NewRouteAnchor(*originLat, *originLng)
	rails := buildRailsFeature(allRibbons, anchor)
	railsFeat := map[string]any{
		"type":         "Feature",
		"properties":   map[string]any{"route": *routeName, "ribbon_count": len(allRibbons), "geometry_mode": "arc"},
		"geometry":     map[string]any{"type": "MultiLineString", "coordinates": rails},
	}

	// Run existing writers for each feature class, parse, append.
	featureLayers, err := collectFeatureLayers(tt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[route-to-geojson] WARN: feature collection: %v\n", err)
	}

	// Emit a LineString per RouteMarker that has a [Start..End] span — these
	// are the "long bars" that highlight platform / siding / junction
	// extents. They use feature_type matching the existing viewer overlays
	// (platform_track / siding_track / junction_track) so the existing
	// styleRibbon styling applies.
	cookedByGUID := make(map[string]*uasset.CookedRibbon, len(allRibbons))
	for i := range allRibbons {
		cookedByGUID[uasset.NormalizeGUID(allRibbons[i].RibbonGUID)] = &allRibbons[i]
	}
	endsByGUID := make(map[string]struct {
		ex, ey, etx, ety float64
		has              bool
	})
	// Reuse the rails endpoint computation by re-running buildRailsFeature's
	// helper logic — easiest: walk all CookedRibbons again storing their
	// resolved end. We just re-run a small variant inline since
	// buildRailsFeature doesn't return endpoint metadata.
	for _, r := range allRibbons {
		key := uasset.NormalizeGUID(r.RibbonGUID)
		if r.Length <= 0 {
			continue
		}
		tn := math.Hypot(r.TangentX, r.TangentY)
		if tn == 0 {
			continue
		}
		if isCurvedArc(&r) {
			dx, dy := geo.ArcDelta(0, 0, r.TangentX/tn, r.TangentY/tn, r.Radius, r.Length)
			endsByGUID[key] = struct {
				ex, ey, etx, ety float64
				has              bool
			}{r.StartX + dx, r.StartY + dy, 0, 0, true}
		} else {
			endsByGUID[key] = struct {
				ex, ey, etx, ety float64
				has              bool
			}{r.StartX + r.TangentX/tn*r.Length, r.StartY + r.TangentY/tn*r.Length, r.TangentX / tn, r.TangentY / tn, true}
		}
	}
	trackFeats := buildTrackFeatureLineStrings(allMarkers, cookedByGUID, endsByGUID, anchor)
	fmt.Fprintf(os.Stderr, "[route-to-geojson] track-feature linestrings: %d (platforms+sidings+junctions)\n", len(trackFeats))

	allFeatures := []any{railsFeat}
	for _, f := range trackFeats {
		allFeatures = append(allFeatures, f)
	}
	for _, f := range featureLayers {
		allFeatures = append(allFeatures, f)
	}
	// Project collectables (world cm) to lat/lng and emit Point features.
	for _, wc := range allCollectables {
		eastM := wc.worldX / 100.0
		southM := wc.worldY / 100.0
		lat, lng := anchor.WorldToLatLng(eastM, southM)
		allFeatures = append(allFeatures, map[string]any{
			"type": "Feature",
			"properties": map[string]any{
				"feature_type":   "collectable",
				"feature_kind":   "collectable",
				"actor_class":    wc.c.ActorClass,
				"instance":       wc.c.InstanceName,
				"collectable_id": wc.c.CollectableID,
				"tile":           wc.c.TileName,
			},
			"geometry": map[string]any{
				"type":        "Point",
				"coordinates": []float64{round7(lng), round7(lat)},
			},
		})
	}
	doc := map[string]any{
		"type":       "FeatureCollection",
		"name":       *routeName,
		"route":      *routeName,
		"origin_lat": *originLat,
		"origin_lng": *originLng,
		"features":   allFeatures,
	}

	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
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
	fmt.Fprintf(os.Stderr, "[route-to-geojson] wrote %s (%d features)\n", *out, len(allFeatures))
}

// autoFindOrigin scans the workdir's persistent-map .uexp files for a
// CentralLatitude/Longitude pair using the existing geo helper.
func autoFindOrigin(workdir string) (lat, lng float64, ok bool) {
	_ = filepath.WalkDir(workdir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || ok {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(p), ".uexp") {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(p), "/")
		if len(parts) < 2 || !strings.EqualFold(parts[len(parts)-2], "Map") {
			return nil
		}
		la, ln, err := geo.ExtractOriginFromUExp(p)
		if err == nil && la != 0 && ln != 0 {
			lat, lng, ok = la, ln, true
		}
		return nil
	})
	return
}

// buildRailsFeature samples every ribbon's curve into a polyline. Clothoids
// fall back to topology-resolved Hermite same as ribbons-to-geojson.
func buildRailsFeature(ribbons []uasset.CookedRibbon, anchor *geo.RouteAnchor) [][][2]float64 {
	type endRef struct {
		ribIdx  int
		atStart bool
	}
	node := map[string][]endRef{}
	for i := range ribbons {
		r := &ribbons[i]
		if r.StartNodeGUID != "" {
			node[r.StartNodeGUID] = append(node[r.StartNodeGUID], endRef{i, true})
		}
		if r.EndNodeGUID != "" {
			node[r.EndNodeGUID] = append(node[r.EndNodeGUID], endRef{i, false})
		}
	}
	type endpoint struct{ ex, ey, etx, ety float64; has bool }
	ends := make([]endpoint, len(ribbons))
	for i := range ribbons {
		r := &ribbons[i]
		if r.Length <= 0 {
			continue
		}
		tn := math.Hypot(r.TangentX, r.TangentY)
		if tn == 0 {
			continue
		}
		r.TangentX /= tn
		r.TangentY /= tn
		switch {
		case isCurvedArc(r):
			dx, dy := geo.ArcDelta(0, 0, r.TangentX, r.TangentY, r.Radius, r.Length)
			ends[i] = endpoint{r.StartX + dx, r.StartY + dy, 0, 0, true}
			sweep := -r.Length / r.Radius
			cs, sn := math.Cos(sweep), math.Sin(sweep)
			ends[i].etx = r.TangentX*cs - r.TangentY*sn
			ends[i].ety = r.TangentX*sn + r.TangentY*cs
		case r.CurveClass != "NetworkCurveClothoidSpiral":
			ends[i] = endpoint{r.StartX + r.TangentX*r.Length, r.StartY + r.TangentY*r.Length, r.TangentX, r.TangentY, true}
		}
	}
	// Resolve clothoid endpoints from topology.
	for i := range ribbons {
		r := &ribbons[i]
		if r.CurveClass != "NetworkCurveClothoidSpiral" {
			continue
		}
		var ex, ey, etx, ety float64
		ok := false
		for _, n := range node[r.EndNodeGUID] {
			if n.ribIdx == i {
				continue
			}
			nb := &ribbons[n.ribIdx]
			if n.atStart {
				ex, ey = nb.StartX, nb.StartY
				etx, ety = nb.TangentX, nb.TangentY
			} else if ends[n.ribIdx].has {
				ex, ey = ends[n.ribIdx].ex, ends[n.ribIdx].ey
				etx, ety = -ends[n.ribIdx].etx, -ends[n.ribIdx].ety
			} else {
				continue
			}
			ok = true
			break
		}
		if ok {
			gap := math.Hypot(ex-r.StartX, ey-r.StartY)
			if gap > r.Length*1.5 {
				ok = false
			}
		}
		if ok {
			ends[i] = endpoint{ex, ey, etx, ety, true}
		} else {
			ends[i] = endpoint{r.StartX + r.TangentX*r.Length, r.StartY + r.TangentY*r.Length, r.TangentX, r.TangentY, true}
		}
	}
	// Sample.
	var out [][][2]float64
	for i := range ribbons {
		r := &ribbons[i]
		if r.Length <= 0 || math.Hypot(r.TangentX, r.TangentY) == 0 {
			continue
		}
		coords := sampleRibbon(r, ends[i], anchor)
		if len(coords) >= 2 {
			out = append(out, coords)
		}
	}
	return out
}

func isCurvedArc(r *uasset.CookedRibbon) bool {
	return r.CurveClass == "NetworkCurveCircularArc" && r.HasRadius && r.Radius != 0 &&
		!math.IsInf(r.Radius, 0) && !math.IsNaN(r.Radius)
}

type ribEnd struct{ ex, ey, etx, ety float64; has bool }

func sampleRibbon(r *uasset.CookedRibbon, e struct{ ex, ey, etx, ety float64; has bool }, anchor *geo.RouteAnchor) [][2]float64 {
	n := int(math.Ceil(r.Length / sampleStepCm))
	if isCurvedArc(r) {
		cn := int(math.Ceil(r.Length / (math.Abs(r.Radius) * maxDeflectionRad)))
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
		case isCurvedArc(r):
			s := t * r.Length
			dx, dy := geo.ArcDelta(0, 0, r.TangentX, r.TangentY, r.Radius, s)
			x, y = r.StartX+dx, r.StartY+dy
		case r.CurveClass == "NetworkCurveClothoidSpiral" && e.has:
			chord := math.Hypot(e.ex-r.StartX, e.ey-r.StartY)
			m := chord / 3.0
			t2 := t * t
			t3 := t2 * t
			h00 := 2*t3 - 3*t2 + 1
			h10 := t3 - 2*t2 + t
			h01 := -2*t3 + 3*t2
			h11 := t3 - t2
			x = h00*r.StartX + h10*r.TangentX*m + h01*e.ex + h11*e.etx*m
			y = h00*r.StartY + h10*r.TangentY*m + h01*e.ey + h11*e.ety*m
		default:
			s := t * r.Length
			x, y = r.StartX+r.TangentX*s, r.StartY+r.TangentY*s
		}
		eastM := x / 100.0
		southM := y / 100.0
		lat, lng := anchor.WorldToLatLng(eastM, southM)
		out = append(out, [2]float64{round7(lng), round7(lat)})
	}
	return out
}

func round7(v float64) float64 { return math.Round(v*1e7) / 1e7 }

// collectFeatureLayers runs each production feature writer and returns the
// resulting Feature objects. Same approach as tc-hermite.
func collectFeatureLayers(tt *uasset.Timetable) ([]json.RawMessage, error) {
	var all []json.RawMessage
	opts := output.DefaultRailsOptions()
	collect := func(write func(io.Writer) error) error {
		var buf bytes.Buffer
		if err := write(&buf); err != nil {
			return err
		}
		if buf.Len() == 0 {
			return nil
		}
		var parsed struct {
			Features []json.RawMessage `json:"features"`
		}
		if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
			return err
		}
		all = append(all, parsed.Features...)
		return nil
	}
	if err := collect(func(w io.Writer) error { _, e := output.WriteTrackFeaturesGeoJSON(w, tt, opts); return e }); err != nil {
		return all, err
	}
	if err := collect(func(w io.Writer) error { _, e := output.WritePlatformsGeoJSON(w, tt); return e }); err != nil {
		return all, err
	}
	if err := collect(func(w io.Writer) error { _, e := output.WriteSignalsGeoJSON(w, tt); return e }); err != nil {
		return all, err
	}
	if err := collect(func(w io.Writer) error { _, e := output.WriteSwitchesGeoJSON(w, tt); return e }); err != nil {
		return all, err
	}
	if err := collect(func(w io.Writer) error { _, e := output.WriteRouteMarkersGeoJSON(w, tt); return e }); err != nil {
		return all, err
	}
	if err := collect(func(w io.Writer) error { _, e := output.WriteCarStopSignsGeoJSON(w, tt); return e }); err != nil {
		return all, err
	}
	return all, nil
}

// satisfy unused-import detector if Go's escape analysis kills `ribEnd`.
var _ ribEnd

// buildTrackFeatureLineStrings emits one LineString per RouteMarker whose
// [Start..End] scalar span is non-zero, by sampling its parent ribbon's
// curve over that range. The output `feature_type` matches the existing
// viewer overlay convention so styleRibbon picks up the colours we already
// have for Platform / Siding / Junction tracks.
func buildTrackFeatureLineStrings(
	markers []*uasset.RouteMarker,
	ribbonsByGUID map[string]*uasset.CookedRibbon,
	endsByGUID map[string]struct {
		ex, ey, etx, ety float64
		has              bool
	},
	anchor *geo.RouteAnchor,
) []any {
	mtToFeatureType := map[string]string{
		"Platform": "platform_track",
		"Siding":   "siding_track",
		"Junction": "junction_track",
	}
	out := make([]any, 0, len(markers))
	for _, m := range markers {
		ft, ok := mtToFeatureType[m.MarkerType]
		if !ok {
			// Unmapped types (Crossing / Stop / etc.) — skip; the point
			// layer would still show them via WriteRouteMarkersGeoJSON.
			continue
		}
		key := uasset.NormalizeGUID(m.RibbonGUID)
		rb := ribbonsByGUID[key]
		if rb == nil || rb.Length <= 0 {
			continue
		}
		// Span: prefer Start..End; if End is zero but Location is set, use a
		// 5%-of-length window centred on Location so the bar is visible.
		startFrac, endFrac := float64(m.Start), float64(m.End)
		if endFrac <= startFrac {
			if m.Location > 0 {
				half := 0.025
				startFrac = float64(m.Location) - half
				endFrac = float64(m.Location) + half
				if startFrac < 0 {
					startFrac = 0
				}
				if endFrac > 1 {
					endFrac = 1
				}
			} else {
				continue
			}
		}
		coords := sampleRibbonRange(rb, endsByGUID[key], startFrac, endFrac, anchor)
		if len(coords) < 2 {
			continue
		}
		out = append(out, map[string]any{
			"type": "Feature",
			"properties": map[string]any{
				"feature_type": ft,
				"feature_kind": "track_feature",
				"location":     m.Name,
				"marker_type":  m.MarkerType,
				"line_side":    m.LineSide,
				"ribbon_guid":  m.RibbonGUID,
				"start":        m.Start,
				"end":          m.End,
			},
			"geometry": map[string]any{
				"type":        "LineString",
				"coordinates": coords,
			},
		})
	}
	return out
}

// sampleRibbonRange samples a ribbon's curve from t=startFrac to t=endFrac
// (both 0..1 along its length) and projects each sample to [lng,lat].
func sampleRibbonRange(
	r *uasset.CookedRibbon,
	e struct {
		ex, ey, etx, ety float64
		has              bool
	},
	startFrac, endFrac float64,
	anchor *geo.RouteAnchor,
) [][2]float64 {
	span := endFrac - startFrac
	if span <= 0 {
		return nil
	}
	rangeLen := span * r.Length
	n := int(math.Ceil(rangeLen / sampleStepCm))
	if isCurvedArc(r) {
		cn := int(math.Ceil(rangeLen / (math.Abs(r.Radius) * maxDeflectionRad)))
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
	tn := math.Hypot(r.TangentX, r.TangentY)
	if tn == 0 {
		return nil
	}
	tx, ty := r.TangentX/tn, r.TangentY/tn
	out := make([][2]float64, 0, n+1)
	for i := 0; i <= n; i++ {
		f := float64(i) / float64(n)
		t := startFrac + f*span
		var x, y float64
		switch {
		case isCurvedArc(r):
			s := t * r.Length
			dx, dy := geo.ArcDelta(0, 0, tx, ty, r.Radius, s)
			x, y = r.StartX+dx, r.StartY+dy
		case r.CurveClass == "NetworkCurveClothoidSpiral" && e.has:
			chord := math.Hypot(e.ex-r.StartX, e.ey-r.StartY)
			m := chord / 3.0
			t2 := t * t
			t3 := t2 * t
			h00 := 2*t3 - 3*t2 + 1
			h10 := t3 - 2*t2 + t
			h01 := -2*t3 + 3*t2
			h11 := t3 - t2
			x = h00*r.StartX + h10*tx*m + h01*e.ex + h11*e.etx*m
			y = h00*r.StartY + h10*ty*m + h01*e.ey + h11*e.ety*m
		default:
			s := t * r.Length
			x, y = r.StartX+tx*s, r.StartY+ty*s
		}
		lat, lng := anchor.WorldToLatLng(x/100.0, y/100.0)
		out = append(out, [2]float64{round7(lng), round7(lat)})
	}
	return out
}
