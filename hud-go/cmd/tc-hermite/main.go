// tc-hermite renders the Training Centre rail map using cubic Hermite
// interpolation for clothoid-spiral ribbons. Straight and circular-arc
// ribbons use the existing analytic walker; only clothoid ribbons get the
// new Hermite treatment.
//
// Why this exists: the production renderer treats clothoids as straight
// chords (their curve asset has no Radius field, so ArcDelta degenerates).
// We have ground-truth recording data showing those segments are smooth
// transition curves. PowersOfA-decoding option proved partial — we need
// a parameter we haven't extracted yet — but Hermite with the four anchors
// the graph already gives us (start_pos, start_tan, end_pos, end_tan)
// produces visually-equivalent curves.
//
// Output: <Desktop>\TrainingCentre_hermite_<ts>.geojson
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
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"hud-go/internal/geo"
	"hud-go/internal/output"
	"hud-go/internal/pak"
	"hud-go/internal/pak/uasset"
)

const (
	tswPath   = `D:\SteamLibrary\steamapps\common\Train Sim World 6`
	repakBin  = `C:\Users\hcfai\Desktop\applications_2\hud-go\repak.exe`
	uassetBin = `C:\Users\hcfai\Desktop\applications_2\hud-go\UAssetGUI.exe`
	// Sampling cadence — 1 sample per metre matches the production renderer
	// and is well under the resolution any viewer can render.
	sampleStepCm        = 100.0
	maxDeflectionRad    = 0.00873 // ~0.5° per polyline segment for tight arcs
	hermiteSamplesMin   = 32
	hermiteSamplesMax   = 600
)

// Module-level config knobs set once from CLI flags in main(). Used by
// buildRibbonPaths' nested walker (closure can't take extra args without
// restructuring) and by the proximity-snap pass.
var (
	cfgChainDot    = 0.85
	cfgAmbigMargin = 0.10
	cfgProxSnapM   = 1.5
	// cfgNoChain disables the path-walker entirely — every ribbon becomes
	// its own LineString. Diagnostic mode for "is the walker the bug?":
	// if this output looks correct (no parallel-track crossings) but the
	// walker output doesn't, the walker is over-chaining. If even this
	// looks wrong, the bug is in individual ribbon geometry, not chaining.
	cfgNoChain bool
	// cfgStrictTrust changes the trust gate from permissive (refuse chain
	// only if both ribbons have trust info AND disagree) to strict (refuse
	// chain if EITHER has trust info AND they're not in the same group).
	// Treats absent trust info as "different group" instead of "unknown."
	cfgStrictTrust bool
	// cfgGroupFilter changes the candidate-selection strategy: when cur is
	// in a trust group, FILTER candidates to only those in the same group
	// before picking by dot product. If no in-group candidates exist, fall
	// back to the unfiltered list. Differs from --strict-trust by picking
	// the best in-group neighbour instead of just refusing the chain when
	// the dot-best happens to be out of group.
	cfgGroupFilter bool

	// cfgClothoidParams: ribbon-GUID (lowercase normalised) -> exact
	// clothoid parameters extracted from NetworkCurveClothoidSpiral. When
	// populated and a clothoid ribbon's GUID is present here, the renderer
	// uses Fresnel-style integration of the clothoid instead of the
	// Hermite-with-inferred-end-tangent approximation. Result: clothoid end
	// position + tangent come from the engine's stored parameters, not from
	// the next ribbon picked by the walker — eliminating walker-induced
	// curve errors in clothoid sections.
	cfgClothoidParams map[string]uasset.ClothoidParams

	// cfgJunctions: map node-GUID -> the 3 valid ribbon GUIDs at a switch
	// (Ingoing/Outgoing/Turnout). Authoritative engine data from
	// NetworkTurnoutJunction. When the walker reaches a node carrying a
	// junction, candidate next-ribbons are restricted to these three. Any
	// other ribbon sharing the node is a different physical track that
	// merely meets here geometrically and must NOT be chained — that's the
	// X-crossing fix.
	cfgJunctions map[string][3]string // node GUID -> {ingoing, outgoing, turnout}

	// cfgRequireJunction: when true and cfgJunctions is loaded, the walker
	// refuses to chain at any node of degree>=3 that lacks junction data.
	// Diagnostic for "are the visible kinks coming from these 'unexplained'
	// multi-ribbon nodes?". Forces a path break there.
	cfgRequireJunction bool

	// cfgNoBoundarySnap disables the midpoint averaging at within-path
	// ribbon boundaries. Diagnostic for "are right-angle elbows caused by
	// the boundary snap creating an artificial vertex when adjacent
	// ribbons don't quite share an endpoint?".
	cfgNoBoundarySnap   bool
	cfgBoundarySnapMaxM float64 // gap above this disables the midpoint snap

	// cfgChainGapMaxM: walker rejects chains where cur's exit and next's
	// entry positions are farther apart than this. 0 = check disabled.
	// Catches parallel-track conflations at yard throats.
	cfgChainGapMaxM float64

	// junctionDebugged: one-shot toggle to print one walker-node-guid sample
	// for format-comparison against the loaded junction map.
	junctionDebugged bool
	// Counters for the junction filter diagnostic: how many nodes the
	// walker visited, how many of those had junction info, how many times
	// the filter actually narrowed the candidate set.
	junctionStatsVisits  int
	junctionStatsMatches int
	junctionStatsNarrow  int

	// cfgRibbonTrust: ribbon-GUID (lowercase normalised) -> set of ribbon
	// GUIDs known to belong to the same physical track piece, harvested
	// from TS tiles' NetworkMultiRibbonRange entries. When populated and
	// BOTH ribbons in a candidate chain appear here, chaining is only
	// permitted if they're in each other's trust set. If EITHER ribbon is
	// missing from the trust map (e.g. its TS tile was too big to parse),
	// we fall back to the dot-product test alone — the constraint is
	// purely additive, never blocking when we have no signal.
	cfgRibbonTrust  map[string]map[string]bool

	// cfgLogChainDecisionsPath: when non-empty, every walker chain attempt
	// writes one CSV row recording the gap, dot product, decision label and
	// involved ribbons. Diagnostic for picking a sane --chain-gap-max-m
	// threshold from data instead of guessing. Histogram of per-decision gap
	// distribution is always printed to stderr at end of buildRibbonPaths.
	cfgLogChainDecisionsPath string
	chainDecisionsFile       *os.File
	// Bucket cutoffs in cm: 10, 50, 100, 200, 500, 1000, 2000, 5000, 10000.
	// Tenth bucket is overflow (>=10000cm = >=100m). chainGapHist counts
	// accepted chains; chainGapHistRej counts every break-with-candidate so
	// we can see the gap distribution of chains the walker rejected.
	chainGapHist    [10]int
	chainGapHistRej [10]int
)

func main() {
	routeFlag := flag.String("route", "TrainingCentre", "route codename (matches DLC pak filename, e.g. WCMLSouth, RivieraLine, BostonProvidence)")
	out := flag.String("out", "", "output GeoJSON path (default: <Desktop>/<Route>_hermite_<ts>.geojson)")
	workDirParent := flag.String("workdir", "", "parent directory under which to create the temp unpack dir (default: system temp). Point to a drive with plenty of free space for big routes (WCML, BoW are 5+ GB unpacked).")
	chainDot := flag.Float64("chain-dot", 0.85, "minimum dot product (0..1) between cur exit tangent and next entry tangent for chaining at a junction. 0.85 ~ 32 deg deviation; 0.95 ~ 18 deg. Bigger values = stricter, fewer cross-track over-chains, more visible breaks.")
	ambigMargin := flag.Float64("ambig-margin", 0.10, "at degree-3+ junctions, the best candidate's dot must beat the second-best by at least this margin. Bigger values = more 3-way junctions left as path boundaries (safer on switch-heavy routes).")
	proxSnapM := flag.Float64("prox-snap-m", 1.5, "proximity-based endpoint snap radius in metres. Smaller values = less risk of pulling unrelated parallel tracks together.")
	ribbonGroupsPath := flag.String("ribbon-groups", "", "optional path to a ribbon-groups JSON (produced by extract_ribbon_groups.py). When provided, chaining requires ribbons to be in the same group; pairs not in the trust map fall back to the dot test. Catches WCML's parallel-track tangling.")
	noChain := flag.Bool("no-chain", false, "DIAGNOSTIC: emit each NetworkRibbon as its own LineString (no walker chaining). Use to A/B-test whether the walker or the ribbon math is the source of visible bugs.")
	strictTrust := flag.Bool("strict-trust", false, "with --ribbon-groups: refuse chain when EITHER ribbon has trust info and they aren't in the same group. Treats missing trust info as 'different group' (more conservative).")
	groupFilter := flag.Bool("group-filter", false, "with --ribbon-groups: when cur is in a trust group, restrict candidates to only ribbons in the same group BEFORE picking the best by dot. Picks the best in-group neighbour instead of refusing the chain when the dot-best is out-of-group.")
	clothoidParamsPath := flag.String("clothoid-params", "", "path to a clothoid-params JSON (produced by extract-clothoid-params). When provided, clothoid ribbons are rendered via Fresnel-style integration of their stored parameters instead of cubic-Hermite with end-tangent inferred from neighbours.")
	junctionsPath := flag.String("junctions", "", "path to a junctions JSON (produced by extract-junctions). When provided, the walker restricts candidate next-ribbons at switch nodes to the three engine-defined connections (Ingoing/Outgoing/Turnout). Eliminates over-chaining at junctions where unrelated parallel tracks happen to share a node.")
	requireJunction := flag.Bool("require-junction", false, "with --junctions: refuse to chain at any node of degree>=3 that doesn't appear in the junctions map. Forces a path break at 'unexplained' multi-ribbon nodes (e.g., crossovers without explicit NetworkTurnoutJunction).")
	noBoundarySnap := flag.Bool("no-boundary-snap", false, "DIAGNOSTIC: disable the midpoint-averaging at within-path ribbon boundaries. The current snap masks ~1m drift between adjacent ribbons by averaging their joining samples; for larger drift it creates 80-90 deg elbows. With this flag we just keep both endpoints (or drop one).")
	boundarySnapMaxM := flag.Float64("boundary-snap-max-m", 0.5, "boundary midpoint-averaging only applies when the gap between adjacent ribbons' joining samples is below this distance (metres). Larger gaps fall back to keeping both vertices.")
	chainGapMaxM := flag.Float64("chain-gap-max-m", 0.0, "walker rejects a chain when the gap between cur's exit position and next's entry position exceeds this distance (metres). 0 disables the check. Try 3-5m to catch parallel-track conflations at yard throats where ribbons share a node guid but are physically apart.")
	logChainDecisions := flag.String("log-chain-decisions", "", "diagnostic: write a CSV row per chain attempt to this path. Columns: node_guid,node_degree,cur_guid,next_guid,gap_m,dot,second_dot,decision. Histogram of accepted/rejected chain gaps is always printed to stderr.")
	flag.Parse()
	cfgChainDot = *chainDot
	cfgAmbigMargin = *ambigMargin
	cfgProxSnapM = *proxSnapM
	cfgNoChain = *noChain
	cfgStrictTrust = *strictTrust
	cfgGroupFilter = *groupFilter
	cfgNoBoundarySnap = *noBoundarySnap
	cfgBoundarySnapMaxM = *boundarySnapMaxM
	cfgChainGapMaxM = *chainGapMaxM
	cfgLogChainDecisionsPath = *logChainDecisions
	fmt.Fprintf(os.Stderr, "[tc-hermite] chain-dot=%.3f ambig-margin=%.3f prox-snap-m=%.2f no-chain=%v strict-trust=%v group-filter=%v boundary-snap-max-m=%.2f no-boundary-snap=%v\n",
		cfgChainDot, cfgAmbigMargin, cfgProxSnapM, cfgNoChain, cfgStrictTrust, cfgGroupFilter, cfgBoundarySnapMaxM, cfgNoBoundarySnap)
	if cfgLogChainDecisionsPath != "" {
		f, err := os.Create(cfgLogChainDecisionsPath)
		if err != nil {
			log.Fatalf("log-chain-decisions: %v", err)
		}
		defer f.Close()
		fmt.Fprintln(f, "node_guid,node_degree,cur_guid,next_guid,gap_m,dot,second_dot,decision")
		chainDecisionsFile = f
		fmt.Fprintf(os.Stderr, "[tc-hermite] logging chain decisions to %s\n", cfgLogChainDecisionsPath)
	}

	if *ribbonGroupsPath != "" {
		data, err := os.ReadFile(*ribbonGroupsPath)
		if err != nil {
			log.Fatalf("ribbon-groups: %v", err)
		}
		var doc struct {
			Trust map[string][]string `json:"trust"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			log.Fatalf("ribbon-groups parse: %v", err)
		}
		cfgRibbonTrust = make(map[string]map[string]bool, len(doc.Trust))
		for k, vs := range doc.Trust {
			m := make(map[string]bool, len(vs))
			for _, v := range vs {
				m[v] = true
			}
			cfgRibbonTrust[k] = m
		}
		fmt.Fprintf(os.Stderr, "[tc-hermite] loaded ribbon-trust for %d ribbons from %s\n",
			len(cfgRibbonTrust), *ribbonGroupsPath)
	}

	if *clothoidParamsPath != "" {
		data, err := os.ReadFile(*clothoidParamsPath)
		if err != nil {
			log.Fatalf("clothoid-params: %v", err)
		}
		var doc struct {
			Params map[string]struct {
				A            float64 `json:"a"`
				L            float64 `json:"l"`
				RadialOffset float64 `json:"radial_offset"`
				StartX       float64 `json:"start_x"`
				StartY       float64 `json:"start_y"`
				StartTanX    float64 `json:"start_tan_x"`
				StartTanY    float64 `json:"start_tan_y"`
			} `json:"params"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			log.Fatalf("clothoid-params parse: %v", err)
		}
		cfgClothoidParams = make(map[string]uasset.ClothoidParams, len(doc.Params))
		for k, v := range doc.Params {
			cfgClothoidParams[k] = uasset.ClothoidParams{
				A:            v.A,
				L:            v.L,
				RadialOffset: v.RadialOffset,
				StartX:       v.StartX,
				StartY:       v.StartY,
				StartTanX:    v.StartTanX,
				StartTanY:    v.StartTanY,
			}
		}
		fmt.Fprintf(os.Stderr, "[tc-hermite] loaded clothoid-params for %d ribbons from %s\n",
			len(cfgClothoidParams), *clothoidParamsPath)
	}

	if *junctionsPath != "" {
		data, err := os.ReadFile(*junctionsPath)
		if err != nil {
			log.Fatalf("junctions: %v", err)
		}
		var doc struct {
			Junctions []struct {
				Node     string `json:"node"`
				Ingoing  string `json:"ingoing"`
				Outgoing string `json:"outgoing"`
				Turnout  string `json:"turnout"`
			} `json:"junctions"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			log.Fatalf("junctions parse: %v", err)
		}
		cfgJunctions = make(map[string][3]string, len(doc.Junctions))
		for _, j := range doc.Junctions {
			cfgJunctions[j.Node] = [3]string{j.Ingoing, j.Outgoing, j.Turnout}
		}
		cfgRequireJunction = *requireJunction
		fmt.Fprintf(os.Stderr, "[tc-hermite] loaded %d junctions from %s (require-junction=%v)\n",
			len(cfgJunctions), *junctionsPath, cfgRequireJunction)
		// Print 2 sample junction-node GUIDs so we can eyeball the format
		// against the node GUIDs the walker hands us.
		i := 0
		for k := range cfgJunctions {
			fmt.Fprintf(os.Stderr, "[tc-hermite]   junction-node-guid sample: %q\n", k)
			i++
			if i >= 2 {
				break
			}
		}
	}

	target := *routeFlag
	outPath := *out
	if outPath == "" {
		outPath = filepath.Join(
			os.Getenv("USERPROFILE"), "Desktop",
			fmt.Sprintf("%s_hermite_%s.geojson", target, time.Now().Format("20060102_150405")),
		)
	}

	routes, err := pak.DiscoverRoutes(tswPath)
	if err != nil {
		log.Fatalf("discover routes: %v", err)
	}
	var route *pak.Route
	for i := range routes {
		if strings.EqualFold(routes[i].Name, target) {
			route = &routes[i]
			break
		}
	}
	if route == nil {
		// List available routes to help typo-debug
		var names []string
		for _, r := range routes {
			names = append(names, r.Name)
		}
		log.Fatalf("route %q not found. available routes: %v", target, names)
	}
	fmt.Fprintf(os.Stderr, "[tc-hermite] route=%s pak=%s\n", route.Name, route.PakPath)

	workDir, err := os.MkdirTemp(*workDirParent, "tc-hermite-*")
	if err != nil {
		log.Fatalf("mkdtemp: %v", err)
	}
	defer os.RemoveAll(workDir)
	fmt.Fprintf(os.Stderr, "[tc-hermite] workdir=%s\n", workDir)

	t0 := time.Now()
	if err := runRepak(route.PakPath, workDir); err != nil {
		log.Fatalf("repak unpack: %v", err)
	}
	fmt.Fprintf(os.Stderr, "[tc-hermite] repak unpack done in %s\n", time.Since(t0).Round(time.Millisecond))

	originLat, originLng := findOrigin(workDir)
	if originLat == 0 && originLng == 0 {
		log.Fatalf("origin lat/lng not found")
	}
	fmt.Fprintf(os.Stderr, "[tc-hermite] origin=(%v, %v)\n", originLat, originLng)
	anchor := geo.NewRouteAnchor(originLat, originLng)

	scan := scanRibbons(workDir)
	ribbons := scan.ribbons
	fmt.Fprintf(os.Stderr, "[tc-hermite] ribbons=%d\n", len(ribbons))

	clothoidCount := 0
	for _, r := range ribbons {
		if r.IsClothoid {
			clothoidCount++
		}
	}
	fmt.Fprintf(os.Stderr, "[tc-hermite] clothoid ribbons: %d / %d\n", clothoidCount, len(ribbons))

	paths := buildRibbonPaths(ribbons)
	fmt.Fprintf(os.Stderr, "[tc-hermite] paths: %d\n", len(paths))
	if cfgJunctions != nil {
		fmt.Fprintf(os.Stderr, "[tc-hermite] junction-filter: visits=%d matches=%d narrowed=%d\n",
			junctionStatsVisits, junctionStatsMatches, junctionStatsNarrow)
	}
	printChainGapHistogram()

	type pathOut struct {
		coords    [][2]float64
		startNode string
		endNode   string
		// (ribGUID, side) the path's start vertex corresponds to. side is
		// "start" or "end", referring to which end of that ribbon the path
		// enters from. Used by the switch-clustering pass below to look up
		// paths by ribbon-end identifier (independent of NodeGUID).
		startRibGUID string
		startRibSide string
		endRibGUID   string
		endRibSide   string
	}
	outs := make([]pathOut, 0, len(paths))
	pathsWithClothoid := 0
	hermitedClothoids := 0
	chordFallbackClothoids := 0
	for _, p := range paths {
		coords, hClo, fClo := buildPathCoords(p, anchor)
		if len(coords) >= 2 {
			sn, en := pathBoundaryNodes(p)
			first := p[0]
			last := p[len(p)-1]
			// At first edge: if role=start, path starts at ribbon's start
			// (=NodeGUID of start_node); if role=end, path starts at ribbon's
			// end (=NodeGUID of end_node). Same logic mirrored for last.
			fSide := "start"
			if first.role == "end" {
				fSide = "end"
			}
			lSide := "end"
			if last.role == "end" {
				lSide = "start"
			}
			outs = append(outs, pathOut{
				coords:       coords,
				startNode:    sn,
				endNode:      en,
				startRibGUID: first.rib.GUID,
				startRibSide: fSide,
				endRibGUID:   last.rib.GUID,
				endRibSide:   lSide,
			})
		}
		if hClo > 0 || fClo > 0 {
			pathsWithClothoid++
			hermitedClothoids += hClo
			chordFallbackClothoids += fClo
		}
	}
	// Snap shared-junction endpoints across paths — without this, two paths
	// meeting at a junction render coordinates that differ by a few cm of
	// float drift, producing visible breaks in the line. The original
	// "first-seen wins" approach picks one path's natural endpoint as
	// canonical for the node, which can drag other paths' endpoints by
	// 5+ m if the first one happens to be a chord-fallback clothoid with
	// a wrong end position. Average all paths' endpoints at each node
	// instead — minimises the max snap displacement.
	type sumPt struct{ x, y float64; n int }
	nodeAcc := map[string]*sumPt{}
	add := func(node string, p [2]float64) {
		if node == "" {
			return
		}
		s, ok := nodeAcc[node]
		if !ok {
			s = &sumPt{}
			nodeAcc[node] = s
		}
		s.x += p[0]
		s.y += p[1]
		s.n++
	}
	for _, o := range outs {
		add(o.startNode, o.coords[0])
		add(o.endNode, o.coords[len(o.coords)-1])
	}
	nodeCoord := map[string][2]float64{}
	for k, s := range nodeAcc {
		nodeCoord[k] = [2]float64{s.x / float64(s.n), s.y / float64(s.n)}
	}
	// Always snap to the averaged canonical coord — visible gaps at
	// junctions look worse than spikes, and a downstream despike pass
	// (smooths spike regions using N points of context on each side)
	// removes any kinks the snap introduces.
	for i := range outs {
		if c, ok := nodeCoord[outs[i].startNode]; ok {
			outs[i].coords[0] = c
		}
		if c, ok := nodeCoord[outs[i].endNode]; ok {
			outs[i].coords[len(outs[i].coords)-1] = c
		}
	}

	// Proximity-based clustering: even after the GUID-keyed snap above, two
	// paths that physically meet at the same junction can render with
	// drift-different endpoint coordinates if the asset's TangentX/Y across
	// the junction made each ribbon claim a slightly different end position.
	// Cluster all path endpoints by proximity (within ~1.5 m) and pull each
	// cluster to its centroid. This eliminates the visible gaps at every
	// junction, including switches where the GUID-keyed snap above couldn't
	// reach because incident paths only share a node geometrically, not by
	// the StartNodeGUID/EndNodeGUID we look up.
	proxSnapMVal := cfgProxSnapM
	type endRef struct {
		pathIdx int
		isStart bool
		coord   [2]float64
	}
	allEnds := make([]endRef, 0, 2*len(outs))
	for i, o := range outs {
		if len(o.coords) < 2 {
			continue
		}
		allEnds = append(allEnds,
			endRef{i, true, o.coords[0]},
			endRef{i, false, o.coords[len(o.coords)-1]})
	}
	// Convert proxSnapMVal to a degree-window for cheap bbox prefilter.
	// 1.5 m at lat ~51° is ~1.4e-5 deg in lat and ~2.1e-5 deg in lng.
	const degLatPerM = 1.0 / 111_000.0
	bandLat := proxSnapMVal * degLatPerM
	merged := make([]bool, len(allEnds))
	for i := range allEnds {
		if merged[i] {
			continue
		}
		bandLng := bandLat / math.Cos(allEnds[i].coord[1]*math.Pi/180)
		members := []int{i}
		merged[i] = true
		for j := i + 1; j < len(allEnds); j++ {
			if merged[j] {
				continue
			}
			dl := allEnds[j].coord[1] - allEnds[i].coord[1]
			dn := allEnds[j].coord[0] - allEnds[i].coord[0]
			if dl > bandLat || dl < -bandLat || dn > bandLng || dn < -bandLng {
				continue
			}
			// Convert to metres for an accurate distance test.
			latM := dl / degLatPerM
			lngM := dn / (degLatPerM / math.Cos(allEnds[i].coord[1]*math.Pi/180))
			if latM*latM+lngM*lngM > proxSnapMVal*proxSnapMVal {
				continue
			}
			members = append(members, j)
			merged[j] = true
		}
		if len(members) < 2 {
			continue
		}
		// Pull the whole cluster to its centroid.
		var sumLng, sumLat float64
		for _, m := range members {
			sumLng += allEnds[m].coord[0]
			sumLat += allEnds[m].coord[1]
		}
		centre := [2]float64{sumLng / float64(len(members)), sumLat / float64(len(members))}
		for _, m := range members {
			er := allEnds[m]
			if er.isStart {
				outs[er.pathIdx].coords[0] = centre
			} else {
				outs[er.pathIdx].coords[len(outs[er.pathIdx].coords)-1] = centre
			}
		}
	}
	// (Switch-based junction clustering was tried in v13 and reverted —
	// it pulled endpoints of chained-through paths whose junction-side
	// was far from the actual switch position, deforming the path.
	// The GUID-keyed + proximity-based snaps above are sufficient.)

	multi := make([][][2]float64, 0, len(outs))
	for _, o := range outs {
		multi = append(multi, o.coords)
	}
	fmt.Fprintf(os.Stderr, "[tc-hermite] paths with clothoids=%d  hermite=%d  chord-fallback=%d\n",
		pathsWithClothoid, hermitedClothoids, chordFallbackClothoids)

	railsFeat := map[string]any{
		"type": "Feature",
		"properties": map[string]any{
			"route":         target,
			"segment_count": len(multi),
			"ribbon_count":  len(ribbons),
			"renderer":      "hermite-clothoid+arc",
			"geometry_mode": "arc", // makes the viewer's styleRibbon() use the merged-rails uniform colour
		},
		"geometry": map[string]any{
			"type":        "MultiLineString",
			"coordinates": multi,
		},
	}

	// Build a uasset.Timetable from the scan so we can reuse the production
	// per-feature writers (platforms / signals / switches / car-stop-signs /
	// route-markers / track-features). Only the fields each writer reads
	// need to be populated.
	tt := &uasset.Timetable{
		Route:           target,
		OriginLat:       originLat,
		OriginLng:       originLng,
		Ribbons:         ribbons,
		RouteRibbons:    ribbons,
		LinkedPlatforms: scan.platforms,
		Signals:         scan.signals,
		Switches:        scan.switches,
		CarStopSigns:    scan.carStopSigns,
		RouteMarkers:    scan.routeMarkers,
	}

	// Use the production writers to convert each feature class into GeoJSON
	// features, then re-merge into our single FeatureCollection so the
	// viewer's existing layer toggles (Platforms / Signals / Switches /
	// platform-tracks / siding-tracks / etc.) all light up.
	extraFeats, err := collectFeatureLayers(tt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[tc-hermite] WARN: feature collection: %v\n", err)
	}
	allFeatures := []any{railsFeat}
	for _, f := range extraFeats {
		allFeatures = append(allFeatures, f)
	}
	doc := map[string]any{
		"type":     "FeatureCollection",
		"name":     "rails-" + target + "-hermite",
		"route":    target,
		"origin_lat": originLat,
		"origin_lng": originLng,
		"features": allFeatures,
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		log.Fatalf("mkdir output: %v", err)
	}
	f, err := os.Create(outPath)
	if err != nil {
		log.Fatalf("create: %v", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		log.Fatalf("encode: %v", err)
	}
	fmt.Fprintf(os.Stderr, "[tc-hermite] wrote %d path(s) to %s\n", len(multi), outPath)
}

// collectFeatureLayers runs each production point/track-feature writer on
// the populated Timetable and returns all of their features merged in
// rendering order: track-feature overlays first (so they sit beneath the
// rails MultiLineString), then platforms / signals / switches / route-
// markers / car-stop-signs.
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

	// Order matters for visual stacking in the viewer (later draws on top).
	if err := collect(func(w io.Writer) error {
		_, e := output.WriteTrackFeaturesGeoJSON(w, tt, opts); return e
	}); err != nil {
		return all, err
	}
	if err := collect(func(w io.Writer) error {
		_, e := output.WritePlatformsGeoJSON(w, tt); return e
	}); err != nil {
		return all, err
	}
	if err := collect(func(w io.Writer) error {
		_, e := output.WriteSignalsGeoJSON(w, tt); return e
	}); err != nil {
		return all, err
	}
	if err := collect(func(w io.Writer) error {
		_, e := output.WriteSwitchesGeoJSON(w, tt); return e
	}); err != nil {
		return all, err
	}
	if err := collect(func(w io.Writer) error {
		_, e := output.WriteRouteMarkersGeoJSON(w, tt); return e
	}); err != nil {
		return all, err
	}
	if err := collect(func(w io.Writer) error {
		_, e := output.WriteCarStopSignsGeoJSON(w, tt); return e
	}); err != nil {
		return all, err
	}
	return all, nil
}

// edgeRole = (ribbon, "start"|"end") — the ribbon's logical orientation in the
// path traversal. role="start" means we entered at the asset's start node.
type edgeRole struct {
	rib  *uasset.Ribbon
	role string
}

// pathBoundaryNodes returns the NetworkNode GUIDs at the start and end of a
// merged ribbon path. Used to snap shared-junction endpoints across paths so
// the rendered MultiLineString doesn't show drift gaps where two paths meet.
func pathBoundaryNodes(path []edgeRole) (start, end string) {
	if len(path) == 0 {
		return "", ""
	}
	first := path[0]
	if first.role == "start" {
		start = first.rib.StartNodeGUID
	} else {
		start = first.rib.EndNodeGUID
	}
	last := path[len(path)-1]
	if last.role == "start" {
		end = last.rib.EndNodeGUID
	} else {
		end = last.rib.StartNodeGUID
	}
	return
}

func runRepak(pakPath, dest string) error {
	cmd := exec.Command(repakBin, "unpack", "--output", dest, pakPath)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runUAssetGUI(uassetPath, jsonPath string) error {
	cmd := exec.Command(uassetBin, "tojson", uassetPath, jsonPath, "VER_UE4_27")
	return cmd.Run()
}

func findOrigin(extractRoot string) (lat, lng float64) {
	var candidates []string
	_ = filepath.WalkDir(extractRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(p), ".uexp") {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(p), "/")
		if len(parts) < 2 || !strings.EqualFold(parts[len(parts)-2], "Map") {
			return nil
		}
		candidates = append(candidates, p)
		return nil
	})
	for _, p := range candidates {
		la, ln, err := geo.ExtractOriginFromUExp(p)
		if err == nil && la != 0 && ln != 0 {
			return la, ln
		}
	}
	return 0, 0
}

// tileScanResult bundles every track-network artefact we pull from the tile
// .umap.json files in a single pass. Ribbons drive the rails geometry;
// the rest are point features the viewer overlays as toggleable layers.
type tileScanResult struct {
	ribbons      map[string]*uasset.Ribbon
	platforms    []*uasset.LinkedPlatform
	signals      []*uasset.Signal
	switches     []*uasset.Switch
	carStopSigns []*uasset.CarStopSign
	routeMarkers []*uasset.RouteMarker
}

func scanRibbons(extractRoot string) tileScanResult {
	res := tileScanResult{ribbons: map[string]*uasset.Ribbon{}}
	var tileAssets []string
	_ = filepath.WalkDir(extractRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(p), ".umap") {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(p), "/")
		if len(parts) < 2 || !strings.EqualFold(parts[len(parts)-2], "Tiles") {
			return nil
		}
		tileAssets = append(tileAssets, p)
		return nil
	})
	fmt.Fprintf(os.Stderr, "[tc-hermite] %d tile umaps\n", len(tileAssets))
	t0 := time.Now()
	for i, ua := range tileAssets {
		jsonPath := ua + ".json"
		if _, err := os.Stat(jsonPath); err != nil {
			if err := runUAssetGUI(ua, jsonPath); err != nil {
				continue
			}
		}
		tileName := strings.TrimSuffix(filepath.Base(ua), ".umap")
		rs, plats, sigs, sws, css, rms, err := uasset.ParseTileFeaturesFromUmap(jsonPath, tileName)
		if err != nil {
			continue
		}
		for _, r := range rs {
			key := uasset.NormalizeGUID(r.GUID)
			if key == "" {
				key = r.GUID
			}
			existing, ok := res.ribbons[key]
			if !ok || (r.HasAnchor && !existing.HasAnchor) {
				res.ribbons[key] = r
			}
		}
		res.platforms = append(res.platforms, plats...)
		res.signals = append(res.signals, sigs...)
		res.switches = append(res.switches, sws...)
		res.carStopSigns = append(res.carStopSigns, css...)
		res.routeMarkers = append(res.routeMarkers, rms...)
		if (i+1)%20 == 0 || i+1 == len(tileAssets) {
			fmt.Fprintf(os.Stderr, "[tc-hermite]   %d/%d tiles parsed (%s)\n",
				i+1, len(tileAssets), time.Since(t0).Round(time.Second))
		}
	}
	fmt.Fprintf(os.Stderr, "[tc-hermite] features: ribbons=%d platforms=%d signals=%d switches=%d car-stop-signs=%d route-markers=%d\n",
		len(res.ribbons), len(res.platforms), len(res.signals), len(res.switches), len(res.carStopSigns), len(res.routeMarkers))
	return res
}

// buildRibbonPaths is a copy of the production graph walker — same role-aware
// junction handling. Kept here because the production version lives in a
// package whose internals we don't want to plumb through a flag for a one-off.
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
			if len(edges) < 2 {
				break // dead end
			}
			// curExitTan is the unit tangent of cur at the shared node in
			// path-traversal direction (the direction the path is moving as
			// it leaves cur). We chain to the candidate edge whose path-entry
			// tangent best aligns with curExitTan — handles degree-2 chains,
			// smooth pairs at degree-3+ junctions (main-line continuation
			// across a switch), and rejects real U-turns / sharp branches.
			_, _, _, curExitTan := edgeTraversalEnds(cur)

			candidates := edges

			// --junctions: at a switch node, the engine defines exactly 3
			// valid ribbons (Ingoing/Outgoing/Turnout). Any other ribbon
			// sharing the node is a different physical track — never a
			// legitimate chain. Authoritative; takes precedence over the
			// group filter and the dot test.
			if cfgJunctions != nil {
				if !junctionDebugged {
					fmt.Fprintf(os.Stderr, "[tc-hermite]   walker-node-guid sample: %q  normalised: %q\n",
						opposite, uasset.NormalizeGUID(opposite))
					junctionDebugged = true
				}
				junctionStatsVisits++
				_, isJunction := cfgJunctions[uasset.NormalizeGUID(opposite)]
				// --require-junction: at degree>=3 nodes lacking junction
				// data, refuse to chain. The engine knows the topology at
				// switches via NetworkTurnoutJunction; nodes without that
				// data are likely yard throats / crossovers / spatial
				// snap-clusters where chaining by tangent dot is unreliable.
				if cfgRequireJunction && len(edges) >= 3 && !isJunction {
					break
				}
				if jc, ok := cfgJunctions[uasset.NormalizeGUID(opposite)]; ok {
					junctionStatsMatches++
					valid := map[string]bool{
						jc[0]: true,
						jc[1]: true,
						jc[2]: true,
					}
					var filtered []edgeRole
					for _, e := range edges {
						if e.rib.GUID == cur.rib.GUID {
							continue
						}
						if valid[uasset.NormalizeGUID(e.rib.GUID)] {
							filtered = append(filtered, e)
						}
					}
					if len(filtered) > 0 && len(filtered) < len(edges)-1 {
						junctionStatsNarrow++
					}
					if len(filtered) > 0 {
						candidates = filtered
					}
				}
			}

			// --group-filter: when cur is in a trust group, restrict
			// candidates to ribbons in the same group BEFORE picking by dot.
			// If filtering would leave no candidates, fall back to all
			// (cur is at a track boundary or the next ribbon isn't grouped).
			if cfgGroupFilter && cfgRibbonTrust != nil {
				curG := uasset.NormalizeGUID(cur.rib.GUID)
				if curTrust, ok := cfgRibbonTrust[curG]; ok {
					var filtered []edgeRole
					for _, e := range candidates {
						if e.rib.GUID == cur.rib.GUID {
							continue
						}
						otherG := uasset.NormalizeGUID(e.rib.GUID)
						if curTrust[otherG] {
							filtered = append(filtered, e)
						}
					}
					if len(filtered) > 0 {
						candidates = filtered
					}
				}
			}

			var bestEdge edgeRole
			bestDot := -2.0
			secondDot := -2.0 // best dot among the OTHER candidates
			for _, e := range candidates {
				if e.rib.GUID == cur.rib.GUID {
					continue
				}
				// At any degree node, traversal role for the next edge is e.role
				// (we enter the next ribbon at the end attached to this node).
				_, _, candEntryTan, _ := edgeTraversalEnds(e)
				dot := curExitTan[0]*candEntryTan[0] + curExitTan[1]*candEntryTan[1]
				if dot > bestDot {
					secondDot = bestDot
					bestDot = dot
					bestEdge = e
				} else if dot > secondDot {
					secondDot = dot
				}
			}
			// Always compute the gap when there's a candidate — used for the
			// chain-decision log + histogram regardless of whether the gap
			// gate is enabled. Cost is negligible (one hypot per iteration).
			var gapCm float64
			haveGap := false
			if bestEdge.rib != nil {
				_, curExit, _, _ := edgeTraversalEnds(cur)
				candEntry, _, _, _ := edgeTraversalEnds(bestEdge)
				gapCm = math.Hypot(curExit[0]-candEntry[0], curExit[1]-candEntry[1])
				haveGap = true
			}
			logDec := func(decision string) {
				if !haveGap {
					return
				}
				idx := gapBucket(gapCm)
				if decision == "chain" {
					chainGapHist[idx]++
				} else {
					chainGapHistRej[idx]++
				}
				if chainDecisionsFile != nil {
					fmt.Fprintf(chainDecisionsFile, "%s,%d,%s,%s,%.4f,%.4f,%.4f,%s\n",
						opposite, len(edges), cur.rib.GUID, bestEdge.rib.GUID,
						gapCm/100.0, bestDot, secondDot, decision)
				}
			}
			// Threshold: dot > 0.85 (~< 32° kink) for any chain. At degree-2
			// nodes there's only one candidate so chaining is unambiguous.
			// At degree-3+ junctions, additionally require the best
			// candidate's dot to be at least 0.10 higher than the
			// second-best (a real switch has the main line aligned with
			// the incoming and the branch deflecting noticeably; if both
			// candidates are similarly aligned, the geometry is too
			// ambiguous to safely chain and we leave the path boundary
			// at the junction). Without this guard the relaxed 0.85
			// threshold occasionally chained onto a switch branch that
			// happened to leave the node in nearly the incoming direction,
			// drawing a visible "tail" past the junction (the X shape
			// where it should be a Y).
			// Diagnostic mode: when --no-chain is set, never chain — every
			// ribbon is its own LineString. Lets us A/B-test whether the
			// visible defects come from walker over-chaining or from
			// individual ribbon geometry.
			if cfgNoChain {
				logDec("no_chain_flag")
				break
			}
			ambiguous := len(edges) >= 3 && (bestDot-secondDot) < cfgAmbigMargin
			if bestDot < cfgChainDot {
				logDec("dot_below_threshold")
				break
			}
			if ambiguous {
				logDec("ambiguous")
				break
			}
			if bestEdge.rib != nil && visited[bestEdge.rib.GUID] {
				logDec("already_visited")
				break
			}
			// Optional position-gap check: when --chain-gap-max-m > 0,
			// reject chains where cur's exit and next's entry are farther
			// apart than the threshold. Catches yard-throat / parallel-
			// track conflations where two ribbons share a node GUID but
			// have physically distinct cached endpoints (typically 5+ m).
			if cfgChainGapMaxM > 0 && haveGap && gapCm > cfgChainGapMaxM*100.0 {
				logDec("gap_reject")
				break
			}
			// Ribbon-trust gate. Two modes:
			//   permissive (default): refuse only when both have trust info
			//     and disagree. Holes in coverage fall back to dot test.
			//   strict: refuse when EITHER has trust info and they aren't in
			//     the same group. Missing trust info is treated as a
			//     different group — more conservative, fewer false chains
			//     at the cost of more visible breaks.
			if cfgRibbonTrust != nil {
				curG := uasset.NormalizeGUID(cur.rib.GUID)
				bestG := uasset.NormalizeGUID(bestEdge.rib.GUID)
				curTrust, curHas := cfgRibbonTrust[curG]
				bestTrust, bestHas := cfgRibbonTrust[bestG]
				if cfgStrictTrust {
					// Strict: chain ONLY if positively in each other's group.
					if curHas && !curTrust[bestG] {
						logDec("trust_strict_cur")
						break
					}
					if bestHas && !bestTrust[curG] {
						logDec("trust_strict_best")
						break
					}
				} else {
					// Permissive: only refuse when both have info and disagree.
					if curHas && bestHas && !curTrust[bestG] {
						logDec("trust_permissive")
						break
					}
				}
			}
			logDec("chain")
			cur = bestEdge
		}
		return path
	}

	junctionNodes := []string{}
	// Diagnostic: node-degree distribution and how many non-degree-2 nodes
	// have NetworkTurnoutJunction info vs how many are "unexplained" (the
	// walker still guesses at those by tangent dot).
	degreeHistogram := map[int]int{}
	nonJunctionAmbig := 0
	knownJunctionAmbig := 0
	for n, edges := range adj {
		degreeHistogram[len(edges)]++
		if len(edges) >= 3 {
			if cfgJunctions != nil {
				if _, ok := cfgJunctions[uasset.NormalizeGUID(n)]; ok {
					knownJunctionAmbig++
				} else {
					nonJunctionAmbig++
				}
			}
		}
		if len(edges) != 2 {
			junctionNodes = append(junctionNodes, n)
		}
	}
	if cfgJunctions != nil {
		fmt.Fprintf(os.Stderr, "[tc-hermite] node-degree histogram: %v\n", degreeHistogram)
		fmt.Fprintf(os.Stderr, "[tc-hermite]   degree>=3 nodes: %d known-junction, %d unexplained\n",
			knownJunctionAmbig, nonJunctionAmbig)
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

// gapBucket maps a chain-decision gap (cm) into one of 10 buckets for the
// stderr histogram. Cutoffs (cm): 10, 50, 100, 200, 500, 1000, 2000, 5000,
// 10000. The tenth bucket is overflow (>=100m). Bucket boundaries chosen to
// give fine resolution in the "real authoring drift" range (sub-metre to a
// few metres) where the threshold-tuning question lives.
func gapBucket(gapCm float64) int {
	cuts := [9]float64{10, 50, 100, 200, 500, 1000, 2000, 5000, 10000}
	for i, c := range cuts {
		if gapCm < c {
			return i
		}
	}
	return 9
}

// gapBucketLabel returns a human-readable label for the i'th bucket.
func gapBucketLabel(i int) string {
	labels := [10]string{
		"<0.10m", "0.10-0.50m", "0.50-1.00m", "1.00-2.00m",
		"2.00-5.00m", "5.00-10.00m", "10.00-20.00m", "20.00-50.00m",
		"50.00-100.00m", ">=100.00m",
	}
	if i < 0 || i >= len(labels) {
		return "?"
	}
	return labels[i]
}

// printChainGapHistogram dumps the accepted-vs-rejected chain gap distribution
// to stderr in two columns. Always called after buildRibbonPaths regardless of
// whether --log-chain-decisions was given — the histogram itself is cheap.
func printChainGapHistogram() {
	var totAcc, totRej int
	for i := 0; i < 10; i++ {
		totAcc += chainGapHist[i]
		totRej += chainGapHistRej[i]
	}
	if totAcc == 0 && totRej == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "[tc-hermite] chain-gap histogram (accepted | rejected, total=%d|%d):\n", totAcc, totRej)
	for i := 0; i < 10; i++ {
		fmt.Fprintf(os.Stderr, "  %-15s %5d | %5d\n", gapBucketLabel(i), chainGapHist[i], chainGapHistRej[i])
	}
}

// haversineMetres returns the great-circle distance in metres between two
// [lng,lat] points. Used by the boundary-snap logic to decide whether two
// adjacent ribbons' joining samples are close enough to safely average to
// their midpoint (small gap) or far enough that midpoint-averaging would
// create a visible elbow (large gap).
func haversineMetres(a, b [2]float64) float64 {
	const R = 6371000.0
	la1 := a[1] * math.Pi / 180
	la2 := b[1] * math.Pi / 180
	dlat := (b[1] - a[1]) * math.Pi / 180
	dlng := (b[0] - a[0]) * math.Pi / 180
	s1, s2 := math.Sin(dlat/2), math.Sin(dlng/2)
	h := s1*s1 + math.Cos(la1)*math.Cos(la2)*s2*s2
	return 2 * R * math.Asin(math.Sqrt(h))
}

// ribbonWorldOrigin returns the world cm of the ribbon's t=0 point.
// Falls back to legacy WorldLocation+StartX if CachedStart is unset.
func ribbonWorldOrigin(rib *uasset.Ribbon) (x, y, z float64) {
	if rib.HasCachedStart {
		return rib.CachedStartX, rib.CachedStartY, rib.CachedStartZ
	}
	return rib.WorldX + rib.StartX, rib.WorldY + rib.StartY, 0
}

// ribbonAssetEnds returns the ribbon's start and end point + tangent in its
// own (asset-authored) orientation, using the analytic arc walker. Tangents
// are unit vectors. Position in world cm.
func ribbonAssetEnds(rib *uasset.Ribbon) (start, end [2]float64, sTan, eTan [2]float64) {
	ox, oy, _ := ribbonWorldOrigin(rib)
	// Normalise tangent to unit
	tn := math.Hypot(rib.TangentX, rib.TangentY)
	if tn == 0 {
		tn = 1
	}
	tx, ty := rib.TangentX/tn, rib.TangentY/tn
	start = [2]float64{ox, oy}
	sTan = [2]float64{tx, ty}
	if rib.Radius != 0 && !math.IsInf(rib.Radius, 0) && !math.IsNaN(rib.Radius) {
		dx, dy := geo.ArcDelta(0, 0, tx, ty, rib.Radius, rib.Length)
		end = [2]float64{ox + dx, oy + dy}
		// Sweep matches ArcDelta convention: -L/R radians (CW from above).
		sweep := -rib.Length / rib.Radius
		cs, sn := math.Cos(sweep), math.Sin(sweep)
		eTan = [2]float64{tx*cs - ty*sn, tx*sn + ty*cs}
	} else {
		// Straight (also the wrong fallback for clothoids — caller must
		// handle clothoids via Hermite using neighbour data).
		end = [2]float64{ox + tx*rib.Length, oy + ty*rib.Length}
		eTan = [2]float64{tx, ty}
	}
	return
}

// edgeTraversalEnds returns (entry, exit) positions and tangents in path-
// traversal direction. For clothoids the values are derived from the asset
// alone (chord fallback) — the caller overrides them via neighbour data
// when this ribbon sits between two non-clothoid edges.
func edgeTraversalEnds(e edgeRole) (entry, exit [2]float64, eTan, xTan [2]float64) {
	s, ee, st, et := ribbonAssetEnds(e.rib)
	if e.role == "start" {
		return s, ee, st, et
	}
	// role=end: flip start/end and negate tangents (we're traversing backwards).
	return ee, s, [2]float64{-et[0], -et[1]}, [2]float64{-st[0], -st[1]}
}

// buildPathCoords concatenates per-ribbon polyline samples for one path,
// using cubic Hermite for clothoid ribbons whose neighbours give us a clean
// (entry_pos, entry_tan, exit_pos, exit_tan) anchor set.
func buildPathCoords(path []edgeRole, anchor *geo.RouteAnchor) ([][2]float64, int, int) {
	if len(path) == 0 {
		return nil, 0, 0
	}
	// Pre-compute per-edge traversal ends (asset-derived). For clothoids,
	// these are chord-based — we'll override via neighbour data below.
	type ends struct {
		entry, exit [2]float64
		eTan, xTan  [2]float64
	}
	pe := make([]ends, len(path))
	for i, e := range path {
		entry, exit, eTan, xTan := edgeTraversalEnds(e)
		pe[i] = ends{entry, exit, eTan, xTan}
	}
	// Override clothoid entry/exit using non-clothoid neighbours when available.
	for i, e := range path {
		if !e.rib.IsClothoid {
			continue
		}
		if i > 0 && !path[i-1].rib.IsClothoid {
			pe[i].entry = pe[i-1].exit
			pe[i].eTan = pe[i-1].xTan
		}
		if i+1 < len(path) && !path[i+1].rib.IsClothoid {
			pe[i].exit = pe[i+1].entry
			pe[i].xTan = pe[i+1].eTan
		}
	}

	out := make([][2]float64, 0)
	hermitedClo, chordFallbackClo := 0, 0
	integratedClo := 0
	for i, e := range path {
		var rc [][2]float64
		if e.rib.IsClothoid {
			// Best path: integrate the actual clothoid params if we have
			// them. Produces a true Fresnel curve with end position +
			// tangent derived from the engine's stored A and L — no
			// inference from neighbours, no dependency on walker decisions
			// for this segment's shape.
			guid := uasset.NormalizeGUID(e.rib.GUID)
			if params, ok := cfgClothoidParams[guid]; ok && params.A != 0 && params.L > 0 {
				rc = sampleClothoid(params, e.role, anchor)
				if len(rc) >= 2 {
					integratedClo++
				}
			}
			if len(rc) < 2 {
				// Hermite fallback (anchored from neighbours).
				origEntry, origExit, origETan, origXTan := edgeTraversalEnds(e)
				used := pe[i]
				anchored := used.entry != origEntry || used.exit != origExit ||
					used.eTan != origETan || used.xTan != origXTan
				rc = sampleHermite(used.entry, used.exit, used.eTan, used.xTan, e.rib.Length, anchor)
				if anchored {
					hermitedClo++
				} else {
					chordFallbackClo++
				}
			}
		} else {
			rc = sampleArc(e, anchor)
		}
		if len(rc) < 2 {
			continue
		}
		if i > 0 && len(out) > 0 {
			// At a within-path ribbon boundary the shared node is represented
			// by two slightly different coordinates: the previous ribbon's
			// last sample and the new ribbon's first sample. For a *small*
			// gap they're effectively the same point (TSW's "ribbon snap
			// radius") and averaging to the midpoint smooths the join.
			//
			// For a *large* gap (>cfgBoundarySnapMaxM), the midpoint sits
			// off the curve and produces visible 80-90 deg elbows when the
			// directions differ. Diagnostic case found in TC: 5 of these
			// elbows around lat 51.117. In that case we drop the duplicate
			// and let the polyline carry through with whatever discrepancy
			// the underlying ribbons authored — preferable to a fake elbow.
			lastIdx := len(out) - 1
			gapM := haversineMetres(out[lastIdx], rc[0])
			if cfgNoBoundarySnap || gapM > cfgBoundarySnapMaxM {
				// Large gap: don't average to a fake midpoint, don't drop
				// a vertex. KEEP BOTH and let the polyline traverse the
				// gap as one straight segment from prev-ribbon's last
				// sample to new-ribbon's first sample. This produces a
				// visible-but-honest line rather than a 90° elbow at a
				// midpoint that lies off both ribbons' actual curves.
				// (Don't modify rc — out already has its last sample, and
				// we'll append rc verbatim including rc[0] as the new
				// segment's start.)
			} else {
				out[lastIdx][0] = (out[lastIdx][0] + rc[0][0]) / 2
				out[lastIdx][1] = (out[lastIdx][1] + rc[0][1]) / 2
				rc = rc[1:]
			}
		}
		out = append(out, rc...)
	}
	_ = integratedClo // reported in caller via stats helper if needed; tracked for future logging
	return out, hermitedClo, chordFallbackClo
}

// sampleClothoid integrates the clothoid analytically (Fresnel-style, via
// trapezoidal numerical integration of cos/sin(heading)) at sampleStepCm
// resolution. Returns world-cm sample points projected to [lng,lat] in
// path-traversal direction (reversed when the edge's role is "end").
//
// The clothoid params live in world cm (the engine stores a "Far" vector
// for high precision over large maps), matching ribbon CachedStartPosition
// — so we project directly via the route anchor without adding a tile
// origin.
func sampleClothoid(p uasset.ClothoidParams, role string, anchor *geo.RouteAnchor) [][2]float64 {
	if p.L <= 0 {
		return nil
	}
	n := int(math.Ceil(p.L / sampleStepCm))
	if n < hermiteSamplesMin {
		n = hermiteSamplesMin
	}
	if n > hermiteSamplesMax {
		n = hermiteSamplesMax
	}
	xs, ys := uasset.IntegrateClothoid(p, n)
	out := make([][2]float64, 0, len(xs))
	for i := range xs {
		eastM := xs[i] / 100.0
		southM := ys[i] / 100.0
		lat, lng := anchor.WorldToLatLng(eastM, southM)
		out = append(out, [2]float64{round7(lng), round7(lat)})
	}
	if role == "end" {
		for a, b := 0, len(out)-1; a < b; a, b = a+1, b-1 {
			out[a], out[b] = out[b], out[a]
		}
	}
	return out
}

// sampleArc walks a non-clothoid ribbon's arc geometry at sampleStepCm
// intervals, plus curvature-aware refinement for tight arcs. Output is in
// path-traversal direction (reversed when role=end).
func sampleArc(e edgeRole, anchor *geo.RouteAnchor) [][2]float64 {
	rib := e.rib
	if rib.Length <= 0 {
		return nil
	}
	n := int(math.Ceil(rib.Length / sampleStepCm))
	if rib.Radius != 0 && !math.IsInf(rib.Radius, 0) && !math.IsNaN(rib.Radius) {
		curveN := int(math.Ceil(rib.Length / (math.Abs(rib.Radius) * maxDeflectionRad)))
		if curveN > n {
			n = curveN
		}
	}
	if n < 2 {
		n = 2
	}
	if n > 5000 {
		n = 5000
	}
	ox, oy, _ := ribbonWorldOrigin(rib)
	tn := math.Hypot(rib.TangentX, rib.TangentY)
	if tn == 0 {
		tn = 1
	}
	tx, ty := rib.TangentX/tn, rib.TangentY/tn
	out := make([][2]float64, 0, n+1)
	for i := 0; i <= n; i++ {
		t := float64(i) / float64(n)
		atLen := t * rib.Length
		dx, dy := geo.ArcDelta(0, 0, tx, ty, rib.Radius, atLen)
		eastM := (ox + dx) / 100.0
		southM := (oy + dy) / 100.0
		lat, lng := anchor.WorldToLatLng(eastM, southM)
		out = append(out, [2]float64{round7(lng), round7(lat)})
	}
	if e.role == "end" {
		for a, b := 0, len(out)-1; a < b; a, b = a+1, b-1 {
			out[a], out[b] = out[b], out[a]
		}
	}
	return out
}

// sampleHermite produces a polyline along a cubic Hermite curve from
// p0 (with tangent t0) to p1 (with tangent t1). Tangent magnitudes use
// chord/3 (Catmull-Rom-style) which matches a real clothoid's midpoint
// deflection within sub-meter accuracy for railway transition curves.
// All inputs in world cm; output in lat/lng.
func sampleHermite(p0, p1 [2]float64, t0, t1 [2]float64, L float64, anchor *geo.RouteAnchor) [][2]float64 {
	chord := math.Hypot(p1[0]-p0[0], p1[1]-p0[1])
	// Magnitude=chord/3 gives a curve whose midpoint deflection from the
	// chord matches a clothoid integrating linearly-changing curvature
	// over the segment. Using L (the arc length) instead overshoots by ~3x.
	tMag := chord / 3.0
	walk := math.Max(L, chord)
	n := int(math.Ceil(walk / sampleStepCm))
	if n < hermiteSamplesMin {
		n = hermiteSamplesMin
	}
	if n > hermiteSamplesMax {
		n = hermiteSamplesMax
	}
	out := make([][2]float64, 0, n+1)
	for i := 0; i <= n; i++ {
		t := float64(i) / float64(n)
		t2 := t * t
		t3 := t2 * t
		h00 := 2*t3 - 3*t2 + 1
		h10 := t3 - 2*t2 + t
		h01 := -2*t3 + 3*t2
		h11 := t3 - t2
		x := h00*p0[0] + h10*t0[0]*tMag + h01*p1[0] + h11*t1[0]*tMag
		y := h00*p0[1] + h10*t0[1]*tMag + h01*p1[1] + h11*t1[1]*tMag
		eastM := x / 100.0
		southM := y / 100.0
		lat, lng := anchor.WorldToLatLng(eastM, southM)
		out = append(out, [2]float64{round7(lng), round7(lat)})
	}
	return out
}

func round7(v float64) float64 {
	return math.Round(v*1e7) / 1e7
}
