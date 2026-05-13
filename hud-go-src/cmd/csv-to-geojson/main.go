// csv-to-geojson — read a CSV of world (x_cm, y_cm) points and emit GeoJSON.
//
// Input columns (header required): looks for "x_cm" and "y_cm". Optional
// "actor" column groups points into LineStrings (one per actor); optional
// "instance_idx" column controls vertex order within a group.
//
// If --mode=points: emits one MultiPoint feature with all points (good for
// dense renders). If --mode=lines (default): emits one LineString per group,
// sorted by instance_idx — much lighter render for thousands of points.
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"hud-go/internal/geo"
)

// snapEndpointsClusterMerge mutates the LineString features in `features`
// in-place: every endpoint within snapM metres of another endpoint joins a
// cluster, and each cluster's members get pulled to the cluster centroid.
// Use case: TSW's NetworkRenderProxyActor polylines are emitted per-actor
// and don't quite touch at tile/actor seams; snapping closes those gaps.
//
// O(n²) over endpoint pairs — fine for thousands of polylines.
func snapEndpointsClusterMerge(features []any, snapM float64) {
	type ep struct {
		featIdx int
		isStart bool
		lng, lat float64
	}
	var eps []ep
	for i, f := range features {
		feat, ok := f.(map[string]any)
		if !ok {
			continue
		}
		geom, ok := feat["geometry"].(map[string]any)
		if !ok {
			continue
		}
		if geom["type"] != "LineString" {
			continue
		}
		coords, ok := geom["coordinates"].([][2]float64)
		if !ok || len(coords) < 2 {
			continue
		}
		eps = append(eps,
			ep{i, true, coords[0][0], coords[0][1]},
			ep{i, false, coords[len(coords)-1][0], coords[len(coords)-1][1]},
		)
	}
	n := len(eps)
	if n < 2 {
		return
	}
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}
	snapM2 := snapM * snapM
	for i := 0; i < n; i++ {
		latRad := eps[i].lat * math.Pi / 180.0
		lngScale := 111000.0 * math.Cos(latRad)
		for j := i + 1; j < n; j++ {
			dlatM := (eps[j].lat - eps[i].lat) * 111000.0
			dlngM := (eps[j].lng - eps[i].lng) * lngScale
			if dlatM*dlatM+dlngM*dlngM < snapM2 {
				union(i, j)
			}
		}
	}
	type acc struct {
		sumLng, sumLat float64
		n              int
	}
	clusters := map[int]*acc{}
	for i, e := range eps {
		r := find(i)
		c := clusters[r]
		if c == nil {
			c = &acc{}
			clusters[r] = c
		}
		c.sumLng += e.lng
		c.sumLat += e.lat
		c.n++
	}
	snapped := 0
	for i, e := range eps {
		r := find(i)
		c := clusters[r]
		if c.n < 2 {
			continue
		}
		cx := c.sumLng / float64(c.n)
		cy := c.sumLat / float64(c.n)
		feat := features[e.featIdx].(map[string]any)
		geom := feat["geometry"].(map[string]any)
		coords := geom["coordinates"].([][2]float64)
		if e.isStart {
			coords[0] = [2]float64{cx, cy}
		} else {
			coords[len(coords)-1] = [2]float64{cx, cy}
		}
		snapped++
	}
	multi := 0
	for _, c := range clusters {
		if c.n >= 2 {
			multi++
		}
	}
	fmt.Fprintf(os.Stderr, "snapped %d endpoints into %d cluster(s) (radius %.2fm)\n",
		snapped, multi, snapM)
}

// nearestNeighborOrder reorders points along a greedy nearest-neighbour path.
// Starts at the point furthest from the centroid (a true endpoint of the
// underlying curve, in almost all real cases — endpoints of a 1D embedded
// curve are the points furthest from its centre of mass), then repeatedly
// picks the unvisited point closest to the last one. Branches in the data
// (e.g. turnouts) get traversed sequentially: one branch end-to-end, then
// the polyline jumps to the other branch.
func nearestNeighborOrder(pts []rowPoint) []rowPoint {
	n := len(pts)
	if n <= 2 {
		return pts
	}
	// Centroid in degrees space (small-area approximation, good enough)
	cx, cy := 0.0, 0.0
	for _, p := range pts {
		cx += p.lng
		cy += p.lat
	}
	cx /= float64(n)
	cy /= float64(n)
	startI := 0
	bestD2 := -1.0
	for i, p := range pts {
		dx := p.lng - cx
		dy := p.lat - cy
		d2 := dx*dx + dy*dy
		if d2 > bestD2 {
			bestD2 = d2
			startI = i
		}
	}
	visited := make([]bool, n)
	out := make([]rowPoint, 0, n)
	cur := startI
	visited[cur] = true
	out = append(out, pts[cur])
	for k := 1; k < n; k++ {
		bestJ := -1
		bestDD := math.Inf(1)
		curP := pts[cur]
		for j := 0; j < n; j++ {
			if visited[j] {
				continue
			}
			dx := pts[j].lng - curP.lng
			dy := pts[j].lat - curP.lat
			d := dx*dx + dy*dy
			if d < bestDD {
				bestDD = d
				bestJ = j
			}
		}
		if bestJ < 0 {
			break
		}
		visited[bestJ] = true
		out = append(out, pts[bestJ])
		cur = bestJ
	}
	return out
}

// rowPoint is one CSV row's projected (lng, lat) plus its grouping key
// and ordering field. Group is the 'actor' column when present (else ""),
// orderIdx the 'instance_idx' column (else 0). For lines mode we sort each
// group by orderIdx ascending.
type rowPoint struct {
	group    string
	orderIdx int
	lng, lat float64
}

func main() {
	in := flag.String("in", "", "input CSV path")
	out := flag.String("out", "", "output GeoJSON path")
	originLat := flag.Float64("origin-lat", 51.11568832397461, "route origin latitude")
	originLng := flag.Float64("origin-lng", 6.209702968597412, "route origin longitude")
	mode := flag.String("mode", "lines", "lines = one LineString per actor (light render); points = one MultiPoint with everything")
	meshFilter := flag.String("mesh-filter", "", "comma-separated substrings; only rows whose 'mesh' column contains one of these are kept. Example: '03_Complete,02_M' to keep centerline sleepers only.")
	orderMode := flag.String("order", "nearest", "vertex order within each group: 'nearest' (greedy nearest-neighbour starting from the most-extreme point — best for tracking rail curves), or 'index' (sort by instance_idx — only correct if HISM placement order is spatial)")
	maxJumpM := flag.Float64("max-jump-m", 3.0, "in 'lines' mode: split a group's polyline at any successive vertex pair > this distance (metres). Catches the case where one HISM holds sleepers from multiple disconnected rails (parallel sidings, branches at turnouts). 0 disables splitting.")
	snapEndpointsM := flag.Float64("snap-endpoints-m", 0.0, "in 'lines' mode: cluster all LineString endpoints within this distance (metres) and pull each cluster to its centroid. Stitches gaps where adjacent actors' polylines almost-but-not-quite meet at tile seams. 0 disables.")
	flag.Parse()
	if *in == "" || *out == "" {
		log.Fatal("usage: csv-to-geojson --in <csv> --out <geojson> [--mode lines|points]")
	}

	f, err := os.Open(*in)
	if err != nil {
		log.Fatalf("open: %v", err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	header, err := r.Read()
	if err != nil {
		log.Fatalf("read header: %v", err)
	}
	xIdx, yIdx, actorIdx, idxIdx, meshIdx := -1, -1, -1, -1, -1
	for i, h := range header {
		switch h {
		case "x_cm":
			xIdx = i
		case "y_cm":
			yIdx = i
		case "actor":
			actorIdx = i
		case "instance_idx":
			idxIdx = i
		case "mesh":
			meshIdx = i
		}
	}
	var meshSubs []string
	if *meshFilter != "" {
		for _, s := range strings.Split(*meshFilter, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				meshSubs = append(meshSubs, s)
			}
		}
		if meshIdx < 0 {
			log.Fatalf("--mesh-filter set but CSV has no 'mesh' column")
		}
	}
	if xIdx < 0 || yIdx < 0 {
		log.Fatalf("header lacks x_cm / y_cm: %v", header)
	}

	anchor := geo.NewRouteAnchor(*originLat, *originLng)
	pts := make([]rowPoint, 0, 64000)
	skipped := 0
	for {
		row, err := r.Read()
		if err != nil {
			break
		}
		if len(meshSubs) > 0 {
			ok := false
			for _, s := range meshSubs {
				if strings.Contains(row[meshIdx], s) {
					ok = true
					break
				}
			}
			if !ok {
				skipped++
				continue
			}
		}
		x, _ := strconv.ParseFloat(row[xIdx], 64)
		y, _ := strconv.ParseFloat(row[yIdx], 64)
		lat, lng := anchor.WorldToLatLng(x/100.0, y/100.0)
		var group string
		if actorIdx >= 0 {
			group = row[actorIdx]
		}
		oi := 0
		if idxIdx >= 0 {
			oi, _ = strconv.Atoi(row[idxIdx])
		}
		pts = append(pts, rowPoint{group, oi, lng, lat})
	}
	if len(meshSubs) > 0 {
		fmt.Fprintf(os.Stderr, "mesh-filter kept %d rows, skipped %d\n", len(pts), skipped)
	}

	var features []any
	switch *mode {
	case "points":
		coords := make([][2]float64, 0, len(pts))
		for _, p := range pts {
			coords = append(coords, [2]float64{p.lng, p.lat})
		}
		features = []any{map[string]any{
			"type":       "Feature",
			"properties": map[string]any{"count": len(coords)},
			"geometry":   map[string]any{"type": "MultiPoint", "coordinates": coords},
		}}
	case "lines":
		// Bucket by group, sort by orderIdx, emit LineString per group with
		// >= 2 points. Single-point groups become Point features (rare).
		groups := map[string][]rowPoint{}
		order := []string{}
		for _, p := range pts {
			if _, ok := groups[p.group]; !ok {
				order = append(order, p.group)
			}
			groups[p.group] = append(groups[p.group], p)
		}
		emit := func(gp []rowPoint, actorTag string, partIdx int) {
			if len(gp) < 2 {
				return
			}
			coords := make([][2]float64, len(gp))
			for i, p := range gp {
				coords[i] = [2]float64{p.lng, p.lat}
			}
			features = append(features, map[string]any{
				"type":       "Feature",
				"properties": map[string]any{"actor": actorTag, "part": partIdx, "n": len(coords)},
				"geometry":   map[string]any{"type": "LineString", "coordinates": coords},
			})
		}
		droppedJump := 0
		for _, g := range order {
			gp := groups[g]
			if len(gp) < 2 {
				continue
			}
			switch *orderMode {
			case "index":
				sort.Slice(gp, func(i, j int) bool { return gp[i].orderIdx < gp[j].orderIdx })
			case "nearest":
				gp = nearestNeighborOrder(gp)
			default:
				log.Fatalf("unknown --order %q (expected 'nearest' or 'index')", *orderMode)
			}
			if *maxJumpM <= 0 {
				emit(gp, g, 0)
				continue
			}
			// Split on segment jumps > maxJumpM
			cur := []rowPoint{gp[0]}
			part := 0
			for i := 1; i < len(gp); i++ {
				dlatM := (gp[i].lat - gp[i-1].lat) * 111000.0
				dlngM := (gp[i].lng - gp[i-1].lng) * 111000.0 *
					math.Cos(gp[i].lat*math.Pi/180.0)
				if math.Hypot(dlatM, dlngM) > *maxJumpM {
					droppedJump++
					emit(cur, g, part)
					part++
					cur = []rowPoint{gp[i]}
				} else {
					cur = append(cur, gp[i])
				}
			}
			emit(cur, g, part)
		}
		if *maxJumpM > 0 {
			fmt.Fprintf(os.Stderr, "split %d polyline jumps > %.2fm\n", droppedJump, *maxJumpM)
		}
		if *snapEndpointsM > 0 {
			snapEndpointsClusterMerge(features, *snapEndpointsM)
		}
	default:
		log.Fatalf("unknown --mode %q (expected lines or points)", *mode)
	}

	doc := map[string]any{
		"type":     "FeatureCollection",
		"name":     "centerline",
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
	fmt.Fprintf(os.Stderr, "wrote %d feature(s) (%d input points) to %s\n",
		len(features), len(pts), *out)
}
