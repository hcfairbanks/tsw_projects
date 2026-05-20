// ribbons-with-rule — walk a cooked route pak's TT_*.umap tiles, sample
// each NetworkRibbon's curve into a polyline, and emit GeoJSON with the
// ribbon's TrackRule asset name attached to every feature so a viewer can
// filter "drivable rail only" by matching the rule.
//
// Pipeline:
//   1. (optional) repak unpack the route's pak to a temp workdir
//   2. Auto-discover the route origin (lat/lng) from the persistent map,
//      unless --origin-lat/--origin-lng are supplied
//   3. Walk every TT_*.umap, decode NetworkRibbon + linked curve via the
//      binary parser, build per-tile ribbon list
//   4. Sample each ribbon into [lng,lat] coordinates using the same arc /
//      clothoid / straight math as internal/cookedmap/rails.go (so the
//      polyline matches the in-game rendered rail to within ~50cm)
//   5. Emit one Feature per ribbon, with `track_rule` set to the ribbon's
//      resolved TrackRule import name (e.g. "BostonProvidenceTrackRule" vs
//      "BostonProvidenceSubwayTrackRule").
//
// Reuses internal/cookedmap rails-builder math (buildRailsFeature in
// rails.go); the only delta is we keep the track_rule on every feature and
// don't bother grouping by service.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"hud-go/internal/devtools"
	"hud-go/internal/geo"
	"hud-go/internal/pak/uasset"
)

const (
	sampleStepCm     = 100.0
	maxDeflectionRad = 0.00873
	minSamples       = 4
	maxSamples       = 5000
)

var repakBin = devtools.MustFindBin("repak")

func main() {
	pakPath := flag.String("pak", "", "path to the route's cooked .pak (will be unpacked to a temp dir)")
	workdir := flag.String("workdir", "", "directory of already-unpacked tiles (recursive). If set, --pak is ignored.")
	out := flag.String("out", "", "output GeoJSON path")
	originLat := flag.Float64("origin-lat", 0, "route origin latitude (auto-discovered if 0)")
	originLng := flag.Float64("origin-lng", 0, "route origin longitude (auto-discovered if 0)")
	keepUnpack := flag.Bool("keep-unpack", false, "don't delete the temp unpack dir on exit")
	flag.Parse()
	if *out == "" {
		log.Fatal("missing --out")
	}
	if *pakPath == "" && *workdir == "" {
		log.Fatal("need either --pak or --workdir")
	}

	tStart := time.Now()

	root := *workdir
	if root == "" {
		tmp, err := os.MkdirTemp("", "ribbons-with-rule-*")
		if err != nil {
			log.Fatalf("mkdtemp: %v", err)
		}
		root = tmp
		t0 := time.Now()
		fmt.Fprintf(os.Stderr, "[ribbons-with-rule] unpacking %s -> %s ...\n", *pakPath, root)
		cmd := exec.Command(repakBin, "unpack", "--output", root, *pakPath)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Fatalf("repak: %v", err)
		}
		fmt.Fprintf(os.Stderr, "[ribbons-with-rule] unpack: %s\n", time.Since(t0).Round(time.Millisecond))
		if !*keepUnpack {
			defer os.RemoveAll(root)
		}
	}

	// Origin discovery — look for Content/Map/<X>Map.uexp (TSW6 modern layout).
	// Reject anything under a Tiles subdir.
	if *originLat == 0 || *originLng == 0 {
		t0 := time.Now()
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || *originLat != 0 {
				return nil
			}
			if !strings.HasSuffix(strings.ToLower(p), ".uexp") {
				return nil
			}
			parts := strings.Split(filepath.ToSlash(p), "/")
			for _, pp := range parts {
				if strings.EqualFold(pp, "Tiles") {
					return nil
				}
			}
			if len(parts) < 3 {
				return nil
			}
			if !strings.EqualFold(parts[len(parts)-3], "Content") {
				return nil
			}
			parent := strings.ToLower(parts[len(parts)-2])
			if parent != "map" && !strings.HasSuffix(parent, "map") {
				return nil
			}
			lat, lng, err := geo.ExtractOriginFromUExp(p)
			if err == nil && lat != 0 && lng != 0 {
				*originLat, *originLng = lat, lng
				fmt.Fprintf(os.Stderr, "[ribbons-with-rule] origin %v,%v from %s\n",
					lat, lng, filepath.Base(p))
			}
			return nil
		})
		fmt.Fprintf(os.Stderr, "[ribbons-with-rule] origin scan: %s\n", time.Since(t0).Round(time.Millisecond))
	}
	if *originLat == 0 || *originLng == 0 {
		log.Fatalf("could not auto-discover origin; supply --origin-lat / --origin-lng")
	}
	anchor := geo.NewRouteAnchor(*originLat, *originLng)

	// Walk TT tiles.
	var tiles []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
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
		tiles = append(tiles, p)
		return nil
	})
	fmt.Fprintf(os.Stderr, "[ribbons-with-rule] %d TT tiles\n", len(tiles))

	t0 := time.Now()
	var features []any
	totals := struct {
		ribbons, withRule, drivable, scenery int
		ruleCounts                           map[string]int
	}{ruleCounts: map[string]int{}}

	for i, tilePath := range tiles {
		tileName := strings.TrimSuffix(filepath.Base(tilePath), ".umap")
		ribs, err := uasset.ParseCookedRibbonsFromUmap(tilePath, tileName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  WARN parse %s: %v\n", tileName, err)
			continue
		}
		feats := sampleTile(ribs, anchor, &totals)
		features = append(features, feats...)
		totals.ribbons += len(ribs)
		for _, r := range ribs {
			if r.TrackRule != "" {
				totals.ruleCounts[r.TrackRule]++
			}
		}
		if (i+1)%50 == 0 || i+1 == len(tiles) {
			fmt.Fprintf(os.Stderr, "  %d/%d tiles, %d features (%s)\n",
				i+1, len(tiles), len(features), time.Since(t0).Round(time.Second))
		}
	}

	doc := map[string]any{
		"type":     "FeatureCollection",
		"name":     "ribbons-with-rule",
		"features": features,
		"metadata": map[string]any{
			"origin_lat":     *originLat,
			"origin_lng":     *originLng,
			"tiles":          len(tiles),
			"ribbons_total":  totals.ribbons,
			"ribbons_with_rule": totals.withRule,
			"track_rule_counts": totals.ruleCounts,
		},
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		log.Fatalf("mkdir out: %v", err)
	}
	f, err := os.Create(*out)
	if err != nil {
		log.Fatalf("create %s: %v", *out, err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	// Compact GeoJSON; the file gets large fast (BPE has ~25k ribbons * 4-10
	// samples each), and a viewer doesn't need pretty-printing.
	if err := enc.Encode(doc); err != nil {
		log.Fatalf("encode: %v", err)
	}
	fmt.Fprintf(os.Stderr, "[ribbons-with-rule] %d features (%d with track_rule) -> %s\n",
		len(features), totals.withRule, *out)
	fmt.Fprintf(os.Stderr, "[ribbons-with-rule] rule breakdown:\n")
	for name, cnt := range totals.ruleCounts {
		fmt.Fprintf(os.Stderr, "    %-50s %d\n", name, cnt)
	}
	fmt.Fprintf(os.Stderr, "[ribbons-with-rule] total: %s\n", time.Since(tStart).Round(time.Millisecond))
}

// --- ribbon sampling (mirrors internal/cookedmap/rails.go) -------------------

type ribbonEnd struct {
	ex, ey, etx, ety float64
	has              bool
}

func isCurvedArc(r *uasset.CookedRibbon) bool {
	return r.CurveClass == "NetworkCurveCircularArc" && r.HasRadius && r.Radius != 0 &&
		!math.IsInf(r.Radius, 0) && !math.IsNaN(r.Radius)
}

func round7(v float64) float64 { return math.Round(v*1e7) / 1e7 }

func sampleTile(ribbons []uasset.CookedRibbon, anchor *geo.RouteAnchor,
	totals *struct {
		ribbons, withRule, drivable, scenery int
		ruleCounts                           map[string]int
	}) []any {
	// Build node graph for clothoid endpoint resolution.
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
	ends := make([]ribbonEnd, len(ribbons))
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
			sweep := -r.Length / r.Radius
			cs, sn := math.Cos(sweep), math.Sin(sweep)
			ends[i] = ribbonEnd{
				ex: r.StartX + dx, ey: r.StartY + dy,
				etx: r.TangentX*cs - r.TangentY*sn,
				ety: r.TangentX*sn + r.TangentY*cs,
				has: true,
			}
		case r.CurveClass != "NetworkCurveClothoidSpiral":
			ends[i] = ribbonEnd{
				ex:  r.StartX + r.TangentX*r.Length,
				ey:  r.StartY + r.TangentY*r.Length,
				etx: r.TangentX,
				ety: r.TangentY,
				has: true,
			}
		}
	}
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
				ex, ey, etx, ety = nb.StartX, nb.StartY, nb.TangentX, nb.TangentY
			} else if ends[n.ribIdx].has {
				ex, ey = ends[n.ribIdx].ex, ends[n.ribIdx].ey
				etx, ety = -ends[n.ribIdx].etx, -ends[n.ribIdx].ety
			} else {
				continue
			}
			ok = true
			break
		}
		if ok && math.Hypot(ex-r.StartX, ey-r.StartY) > r.Length*1.5 {
			ok = false
		}
		if ok {
			ends[i] = ribbonEnd{ex: ex, ey: ey, etx: etx, ety: ety, has: true}
		} else {
			ends[i] = ribbonEnd{
				ex:  r.StartX + r.TangentX*r.Length,
				ey:  r.StartY + r.TangentY*r.Length,
				etx: r.TangentX,
				ety: r.TangentY,
				has: true,
			}
		}
	}

	out := make([]any, 0, len(ribbons))
	for i := range ribbons {
		r := &ribbons[i]
		if r.Length <= 0 || math.Hypot(r.TangentX, r.TangentY) == 0 {
			continue
		}
		coords := sampleRibbon(r, ends[i], anchor)
		if len(coords) < 2 {
			continue
		}
		if r.TrackRule != "" {
			totals.withRule++
		}
		out = append(out, map[string]any{
			"type": "Feature",
			"properties": map[string]any{
				"ribbon":     r.RibbonName,
				"track_rule": r.TrackRule,
				"class":      r.CurveClass,
				"length_cm":  r.Length,
			},
			"geometry": map[string]any{"type": "LineString", "coordinates": coords},
		})
	}
	return out
}

func sampleRibbon(r *uasset.CookedRibbon, e ribbonEnd, anchor *geo.RouteAnchor) [][2]float64 {
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
		lat, lng := anchor.WorldToLatLng(x/100.0, y/100.0)
		out = append(out, [2]float64{round7(lng), round7(lat)})
	}
	return out
}
