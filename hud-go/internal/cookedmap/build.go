// Package cookedmap builds a route-level GeoJSON FeatureCollection from
// an unpacked TSW6 pak directory using the in-process cooked-pak readers
// (no UAssetGUI / JSON-conversion roundtrip).
//
// Output shape mirrors the existing route_<RouteDisplay>.json file the
// extractor zip ships today: rails (MultiLineString) + track-feature
// LineStrings + per-feature Point layers (platforms, signals, switches,
// route markers, cab-stop signs) + collectables. Top-level `route`,
// `name`, `origin_lat/lng`, `country`, `country_code` and
// `cross_pak_reference_name` fields stay drop-in compatible with the
// importer.
package cookedmap

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math"
	"path/filepath"
	"strings"
	"time"

	"hud-go/internal/geo"
	"hud-go/internal/output"
	"hud-go/internal/pak/uasset"
)

// Options configures a Build run. Workdir is the unpacked-pak directory
// (the same shape extractor.Extract produces under workDir/<RouteName>);
// RouteName feeds the rails feature's `route` property; OriginLat/Lng are
// the route's anchor in degrees (zero = autodetect from the workdir).
type Options struct {
	Workdir   string
	RouteName string

	OriginLat float64
	OriginLng float64

	// Optional metadata embedded at the top of the FeatureCollection.
	// Empty strings are simply omitted from the output.
	DisplayName           string
	Country               string
	CountryCode           string
	CrossPakReferenceName string

	// Logger receives printf-style progress lines. nil = silent.
	Logger func(format string, args ...any)
}

// Stats reports counts and timing for one Build run.
type Stats struct {
	TTTiles      int
	UmapsScanned int
	Ribbons      int
	Platforms    int
	Signals      int
	Switches     int
	CarStopSigns int
	RouteMarkers int
	Collectables int
	Features     int
	Elapsed      time.Duration
}

// Build walks Workdir, parses the cooked binaries, and emits the
// route-level GeoJSON FeatureCollection to w.
func Build(opts Options, w io.Writer) (Stats, error) {
	t0 := time.Now()
	logf := opts.Logger
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if opts.Workdir == "" {
		return Stats{}, fmt.Errorf("cookedmap: Workdir is required")
	}

	originLat, originLng := opts.OriginLat, opts.OriginLng
	if originLat == 0 || originLng == 0 {
		la, ln, ok := AutoFindOrigin(opts.Workdir)
		if !ok {
			return Stats{}, fmt.Errorf("cookedmap: origin not provided and auto-detect failed")
		}
		originLat, originLng = la, ln
		logf("[cookedmap] auto-detected origin: (%.10f, %.10f)\n", la, ln)
	}

	// Pass 1: TT_*.umap → ribbons + features (rails geometry source).
	var ttTiles []string
	_ = filepath.WalkDir(opts.Workdir, func(p string, d fs.DirEntry, err error) error {
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
		ttTiles = append(ttTiles, p)
		return nil
	})
	logf("[cookedmap] %d TT tiles\n", len(ttTiles))

	allRibbons := []uasset.CookedRibbon{}
	allPlatforms := []*uasset.LinkedPlatform{}
	allSignals := []*uasset.Signal{}
	allSwitches := []*uasset.Switch{}
	allStops := []*uasset.CarStopSign{}
	allMarkers := []*uasset.RouteMarker{}
	for _, p := range ttTiles {
		tileName := strings.TrimSuffix(filepath.Base(p), ".umap")
		if ribs, err := uasset.ParseCookedRibbonsFromUmap(p, tileName); err == nil {
			allRibbons = append(allRibbons, ribs...)
		}
		if fts, err := uasset.ParseCookedFeaturesFromUmap(p, tileName); err == nil && fts != nil {
			allPlatforms = append(allPlatforms, fts.Platforms...)
			allSignals = append(allSignals, fts.Signals...)
			allSwitches = append(allSwitches, fts.Switches...)
			allStops = append(allStops, fts.CarStopSigns...)
			allMarkers = append(allMarkers, fts.RouteMarkers...)
		}
	}
	logf("[cookedmap] parsed: %d ribbons, %d platforms, %d signals, %d switches, %d car-stop-signs, %d route-markers\n",
		len(allRibbons), len(allPlatforms), len(allSignals), len(allSwitches), len(allStops), len(allMarkers))

	// Pass 2: every .umap → collectables (CollectableID GUID present).
	type worldCollectable struct {
		c              *uasset.CookedCollectable
		worldX, worldY float64
	}
	var allCollectables []worldCollectable
	var allUmaps []string
	_ = filepath.WalkDir(opts.Workdir, func(p string, d fs.DirEntry, err error) error {
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
	logf("[cookedmap] collectables: %d across %d umaps\n", len(allCollectables), len(allUmaps))

	// Adapt CookedRibbons → uasset.Ribbon for the existing per-feature
	// writers (signals/switches/cab-stops/route-markers). They look up
	// ribbon CachedStartX/Y to convert ribbon-local offsets into world cm.
	ribbonsByGUID := map[string]*uasset.Ribbon{}
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
			CachedStartZ:   cr.StartZ,
			HasCachedStart: true,
			IsClothoid:     cr.CurveClass == "NetworkCurveClothoidSpiral",
		}
		ribbonsByGUID[uasset.NormalizeGUID(r.GUID)] = r
	}
	tt := &uasset.Timetable{
		Route:           opts.RouteName,
		OriginLat:       originLat,
		OriginLng:       originLng,
		Ribbons:         ribbonsByGUID,
		RouteRibbons:    ribbonsByGUID,
		LinkedPlatforms: allPlatforms,
		Signals:         allSignals,
		Switches:        allSwitches,
		CarStopSigns:    allStops,
		RouteMarkers:    allMarkers,
	}

	// Geometry — rails MultiLineString, then track-feature LineStrings
	// (platform/siding/junction extents) so they layer beneath the rails.
	anchor := geo.NewRouteAnchor(originLat, originLng)
	rails := buildRailsFeature(allRibbons, anchor)
	railsFeat := map[string]any{
		"type": "Feature",
		"properties": map[string]any{
			"route":         opts.RouteName,
			"ribbon_count":  len(allRibbons),
			"geometry_mode": "arc",
		},
		"geometry": map[string]any{"type": "MultiLineString", "coordinates": rails},
	}

	// Per-feature Point layers — re-use the existing GeoJSON writers.
	pointFeats, err := collectFeatureLayers(tt)
	if err != nil {
		logf("[cookedmap] WARN: feature collection: %v\n", err)
	}

	cookedByGUID := make(map[string]*uasset.CookedRibbon, len(allRibbons))
	for i := range allRibbons {
		cookedByGUID[uasset.NormalizeGUID(allRibbons[i].RibbonGUID)] = &allRibbons[i]
	}
	endsByGUID := make(map[string]ribbonEnd)
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
			endsByGUID[key] = ribbonEnd{ex: r.StartX + dx, ey: r.StartY + dy, has: true}
		} else {
			endsByGUID[key] = ribbonEnd{
				ex:  r.StartX + r.TangentX/tn*r.Length,
				ey:  r.StartY + r.TangentY/tn*r.Length,
				etx: r.TangentX / tn,
				ety: r.TangentY / tn,
				has: true,
			}
		}
	}
	trackFeats := buildTrackFeatureLineStrings(allMarkers, cookedByGUID, endsByGUID, anchor)
	logf("[cookedmap] track-feature linestrings: %d\n", len(trackFeats))

	allFeatures := []any{railsFeat}
	for _, f := range trackFeats {
		allFeatures = append(allFeatures, f)
	}
	for _, f := range pointFeats {
		allFeatures = append(allFeatures, f)
	}
	for _, wc := range allCollectables {
		lat, lng := anchor.WorldToLatLng(wc.worldX/100.0, wc.worldY/100.0)
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

	displayName := opts.DisplayName
	if displayName == "" {
		displayName = opts.RouteName
	}
	doc := map[string]any{
		"type":       "FeatureCollection",
		"name":       displayName,
		"route":      opts.RouteName,
		"origin_lat": originLat,
		"origin_lng": originLng,
		"features":   allFeatures,
	}
	if opts.CrossPakReferenceName != "" {
		doc["cross_pak_reference_name"] = opts.CrossPakReferenceName
	}
	if opts.Country != "" {
		doc["country"] = opts.Country
	}
	if opts.CountryCode != "" {
		doc["country_code"] = opts.CountryCode
	}

	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return Stats{}, err
	}

	return Stats{
		TTTiles:      len(ttTiles),
		UmapsScanned: len(allUmaps),
		Ribbons:      len(allRibbons),
		Platforms:    len(allPlatforms),
		Signals:      len(allSignals),
		Switches:     len(allSwitches),
		CarStopSigns: len(allStops),
		RouteMarkers: len(allMarkers),
		Collectables: len(allCollectables),
		Features:     len(allFeatures),
		Elapsed:      time.Since(t0),
	}, nil
}

// AutoFindOrigin scans the workdir's persistent-map .uexp files for a
// CentralLatitude/Longitude pair. Useful for the CLI; server callers
// already have origin from the route definition.
func AutoFindOrigin(workdir string) (lat, lng float64, ok bool) {
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

// collectFeatureLayers runs each existing per-feature GeoJSON writer and
// returns the resulting Feature objects (parsed back out so we can splice
// them into the combined route_<X>.json).
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

