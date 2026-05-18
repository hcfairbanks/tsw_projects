// service_path_datatrack.go — build a service's path from the pre-baked
// per-service TrackData breadcrumbs the game uses internally, instead of
// the proximity-based BuildServicePath walker.
//
// Why this builder exists
//
// The user-facing schedule (Service.Schedule) only carries the stops and
// any explicit GO-VIA points — typically 3–20 waypoints for a commuter
// run. Between waypoints the previous path-builder ran a Dijkstra over the
// ribbon graph weighted by length, which silently mispicks at parallel-
// ribbon stretches (NEC approach to Back Bay, station ladders, yard
// throats).
//
// The RouteTimetableDataTrack uasset that ships next to every timetable
// has the actual answer: a per-service `TrackData` array that lists every
// (ribbon, fraction) the service crosses, in order, with cumulative
// distance and direction. For MBTA Franklin #741 that's 161 breadcrumbs;
// for Amtrak Acela #2155 it's 449. uasset.ServiceTrackData.TrackData is
// what we parse out of it.
//
// How the breadcrumbs translate to a polyline
//
// Breadcrumbs land at signal-section boundaries, not at every ribbon
// transition. Two consecutive breadcrumbs on different ribbons can be
// separated by one or more "intermediate" ribbons (typically switch
// crossovers between signal sections) that aren't themselves emitted as
// breadcrumbs. So for each consecutive pair we use the same
// `samplePathBetween` helper the proximity walker uses:
//
//   - same ribbon → sample the arc from prev.frac to cur.frac
//   - different ribbons → Dijkstra on the ribbon graph from
//     (prev.rib, prev.frac) to (cur.rib, cur.frac), sampling every
//     ribbon in the chain
//
// The Dijkstra here is much more constrained than the schedule-only
// version: each hop is at most a few hundred metres because the
// breadcrumbs constrain the corridor at every step. So even though
// Dijkstra alone mispicks at parallel ribbons, with the breadcrumbs in
// front of it it can only divert for at most one signal section before
// the next breadcrumb pulls it back to the correct track.

package output

import (
	"hud-go/internal/geo"
	"hud-go/internal/pak/uasset"
)

// BuildServicePathFromTrackData resolves a service's path from the
// DataTrack breadcrumb list.
//
// `vertices` is the rails-builder per-ribbon vertex map
// (Stats.RibbonVertices, surfaced via Timetable.RibbonVertices). Required
// for the result to match the rendered rails layer on clothoid spirals.
//
// `switches` is the route's NetworkTurnoutJunction records — used by
// `samplePathBetween`/`buildNodeAdjacency` to stitch ribbons together at
// junctions where per-ribbon Start/EndNodeGUIDs drift. Pass nil only when
// you've verified the route has none (rare).
//
// Returns nil when td is empty, the ribbon catalog has no usable matches,
// or the anchor is missing. In that case the caller should fall back to
// BuildServicePath.
func BuildServicePathFromTrackData(td []uasset.TrackDataEvent, ribbons map[string]*uasset.Ribbon, switches []*uasset.Switch, vertices map[string][][2]float64, anchor *geo.RouteAnchor) []ServiceCoord {
	if anchor == nil || len(ribbons) == 0 || len(td) == 0 {
		return nil
	}
	// Resolve every breadcrumb's ribbon up front so the inner loop is just
	// graph work. Drop any entry whose ribbon isn't in the catalog
	// (extracted pak was incomplete, or the breadcrumb refers to a sibling
	// DLC's ribbon that wasn't pulled in — both rare but possible).
	type point struct {
		rib  *uasset.Ribbon
		frac float64
	}
	pts := make([]point, 0, len(td))
	for i := range td {
		key := uasset.NormalizeGUID(td[i].RibbonGUID)
		if key == "" {
			key = td[i].RibbonGUID
		}
		rib, ok := ribbons[key]
		if !ok || rib == nil || rib.Length <= 0 {
			continue
		}
		f := float64(td[i].RibbonLocation)
		if f < 0 {
			f = 0
		}
		if f > 1 {
			f = 1
		}
		pts = append(pts, point{rib: rib, frac: f})
	}
	if len(pts) == 0 {
		return nil
	}

	adj := buildNodeAdjacency(ribbons, switches)
	out := []ServiceCoord{}
	// Seed with the first breadcrumb's snapped position so the polyline
	// starts on a rail vertex, mirroring waypointSeed in the analytic
	// builder.
	out = append(out, waypointSeed(pts[0].rib, pts[0].frac, vertices, anchor))

	for i := 1; i < len(pts); i++ {
		prev, cur := pts[i-1], pts[i]
		seg := samplePathBetween(adj, ribbons, vertices, anchor, prev.rib, prev.frac, cur.rib, cur.frac)
		// Drop the leading vertex — it duplicates the last vertex already
		// appended (either the seed or the previous segment's tail).
		if len(seg) > 0 {
			seg = seg[1:]
		}
		out = append(out, seg...)
	}
	return out
}
