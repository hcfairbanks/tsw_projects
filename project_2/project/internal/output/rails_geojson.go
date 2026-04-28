package output

import (
	"math"

	"tsw6-timetable/internal/geo"
	"tsw6-timetable/internal/pak/uasset"
)

// RailsGeoJSONOptions controls arc-sampling for the merged rails writer.
type RailsGeoJSONOptions struct {
	// SampleStepMeters: linear distance between samples along each ribbon's arc.
	// Smaller = smoother curves but bigger files. 5-20 m is a good range.
	SampleStepMeters float64
	// MinSamples: minimum number of points per ribbon (a tiny ribbon still gets
	// at least the start + end). Default 2.
	MinSamples int
	// MaxSamples: hard ceiling per ribbon to bound output size. Default 400.
	MaxSamples int
}

// DefaultRailsOptions returns reasonable defaults for the rails writer.
// 5 m step + curvature refinement keeps every visible curve smooth at typical
// zoom levels without bloating the file.
func DefaultRailsOptions() RailsGeoJSONOptions {
	return RailsGeoJSONOptions{
		SampleStepMeters: 5,
		MinSamples:       2,
		MaxSamples:       400,
	}
}

// maxDeflectionRadians caps the angular change per polyline segment so
// tight-radius curves don't render as flat polygons. ~3° per segment looks
// smooth at every normal zoom level.
const maxDeflectionRadians = 0.05236

// sampleRibbonLatLng walks a ribbon's arc geometry and returns a slice of
// [lng, lat] pairs ready for GeoJSON. Returns nil for ribbons with no usable
// geometry.
func sampleRibbonLatLng(rib *uasset.Ribbon, anchor *geo.RouteAnchor, opts RailsGeoJSONOptions) [][2]float64 {
	if rib == nil || rib.Length <= 0 {
		return nil
	}
	lengthCm := rib.Length
	stepCm := opts.SampleStepMeters * 100.0
	if stepCm <= 0 {
		stepCm = 1000.0
	}
	n := int(math.Ceil(lengthCm / stepCm))
	// Curvature-aware refinement: for an arc of radius R the total swept
	// angle is L/|R|. To keep each segment under maxDeflectionRadians, need
	// at least L/(|R| * maxDeflection) segments. Straight ribbons (R=0 or
	// non-finite) skip this entirely.
	if rib.Radius != 0 && !math.IsInf(rib.Radius, 0) && !math.IsNaN(rib.Radius) {
		curveN := int(math.Ceil(lengthCm / (math.Abs(rib.Radius) * maxDeflectionRadians)))
		if curveN > n {
			n = curveN
		}
	}
	if n+1 < opts.MinSamples {
		n = opts.MinSamples - 1
	}
	if n+1 > opts.MaxSamples {
		n = opts.MaxSamples - 1
	}
	if n < 1 {
		n = 1
	}
	originX, originY, _ := ribbonWorldOrigin(rib)
	out := make([][2]float64, 0, n+1)
	for i := 0; i <= n; i++ {
		t := float64(i) / float64(n)
		atLen := t * lengthCm
		// ArcDelta is frame-invariant — the displacement from the curve start
		// only depends on tangent/radius/length, not on StartX/Y. So we walk
		// the curve from (0,0) and add the world-space origin separately.
		dx, dy := geo.ArcDelta(0, 0, rib.TangentX, rib.TangentY, rib.Radius, atLen)
		eastM := (originX + dx) / 100.0
		southM := (originY + dy) / 100.0
		lat, lng := anchor.WorldToLatLng(eastM, southM)
		out = append(out, [2]float64{round7(lng), round7(lat)})
	}
	return out
}

// ribbonWorldOrigin returns the world-space (X, Y, Z) of the ribbon's t=0
// point in UE cm. Prefers CachedStartPosition (the actual world anchor on
// every TSW6 NetworkRibbon record). Falls back to legacy WorldLocation +
// curve StartX/Y for older index entries that predate the
// CachedStartPosition parser.
func ribbonWorldOrigin(rib *uasset.Ribbon) (x, y, z float64) {
	if rib.HasCachedStart {
		return rib.CachedStartX, rib.CachedStartY, rib.CachedStartZ
	}
	return rib.WorldX + rib.StartX, rib.WorldY + rib.StartY, 0
}

func round7(v float64) float64 {
	return math.Round(v*1e7) / 1e7
}
