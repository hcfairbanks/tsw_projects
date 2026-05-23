package cookedmap

import (
	"math"

	"hud-go/internal/geo"
	"hud-go/internal/pak/uasset"
)

// Rail-sampling tunables — same values as the original cmd/route-to-geojson.
const (
	sampleStepCm     = 100.0
	maxDeflectionRad = 0.00873
	minSamples       = 4
	maxSamples       = 5000
)

// ribbonEnd records each ribbon's resolved endpoint (world cm) and the
// outgoing tangent. Used by track-feature LineString sampling.
type ribbonEnd struct {
	ex, ey, etx, ety float64
	has              bool
}

func round7(v float64) float64 { return math.Round(v*1e7) / 1e7 }

// anyNonEmpty reports whether any string in s is non-empty. Used to decide
// whether the rails feature carries usable TrackRule data worth emitting.
func anyNonEmpty(s []string) bool {
	for _, v := range s {
		if v != "" {
			return true
		}
	}
	return false
}

func isCurvedArc(r *uasset.CookedRibbon) bool {
	return r.CurveClass == "NetworkCurveCircularArc" && r.HasRadius && r.Radius != 0 &&
		!math.IsInf(r.Radius, 0) && !math.IsNaN(r.Radius)
}

// buildRailsFeature samples every ribbon's curve into a polyline. Clothoid
// endpoints are resolved through the topology graph (next ribbon's start
// point) so the spline lands on the network rather than overshooting.
//
// Returns the multi-line array (each sub-line one ribbon, in caller-order),
// a per-ribbon-GUID map of the SAME vertex arrays so the per-service
// path-builder can slice from them (keys are normalised ribbon GUIDs), AND
// a slice of TrackRule asset names parallel to the multi-line array (one per
// emitted sub-line — e.g. "BostonProvidenceTrackRule" vs
// "BostonProvidenceSubwayTrackRule"; "" when the ribbon shipped no rule).
// The route map uses the rule names to offer a "Drivable Rail only" filter.
// Ribbons that fail the geometry checks (zero length / zero tangent) are
// absent from all outputs, so a missing key means "no rail geometry for
// this ribbon" and the path-builder must treat it as a break.
func buildRailsFeature(ribbons []uasset.CookedRibbon, anchor *geo.RouteAnchor) ([][][2]float64, map[string][][2]float64, []string) {
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
			ends[i] = ribbonEnd{ex: r.StartX + dx, ey: r.StartY + dy, has: true}
			sweep := -r.Length / r.Radius
			cs, sn := math.Cos(sweep), math.Sin(sweep)
			ends[i].etx = r.TangentX*cs - r.TangentY*sn
			ends[i].ety = r.TangentX*sn + r.TangentY*cs
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
	var out [][][2]float64
	var rules []string
	perRibbon := make(map[string][][2]float64, len(ribbons))
	for i := range ribbons {
		r := &ribbons[i]
		if r.Length <= 0 || math.Hypot(r.TangentX, r.TangentY) == 0 {
			continue
		}
		coords := sampleRibbon(r, ends[i], anchor)
		if len(coords) >= 2 {
			out = append(out, coords)
			rules = append(rules, r.TrackRule)
			perRibbon[uasset.NormalizeGUID(r.RibbonGUID)] = coords
		}
	}
	return out, perRibbon, rules
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

// sampleRibbonRange samples a ribbon's curve from t=startFrac to t=endFrac
// (both 0..1 along its length) and projects each sample to [lng,lat].
func sampleRibbonRange(r *uasset.CookedRibbon, e ribbonEnd, startFrac, endFrac float64, anchor *geo.RouteAnchor) [][2]float64 {
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

// buildTrackFeatureLineStrings emits one LineString per RouteMarker whose
// [Start..End] scalar span is non-zero, by sampling its parent ribbon's
// curve over that range. Output `feature_type` matches the existing viewer
// overlay convention (platform_track / siding_track / junction_track).
func buildTrackFeatureLineStrings(
	markers []*uasset.RouteMarker,
	ribbonsByGUID map[string]*uasset.CookedRibbon,
	endsByGUID map[string]ribbonEnd,
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
			continue
		}
		key := uasset.NormalizeGUID(m.RibbonGUID)
		rb := ribbonsByGUID[key]
		if rb == nil || rb.Length <= 0 {
			continue
		}
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
