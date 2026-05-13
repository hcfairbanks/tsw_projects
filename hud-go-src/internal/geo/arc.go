package geo

import "math"

// ArcDelta returns the (Δx, Δy) from the start of a circular or straight-line
// ribbon segment after travelling `lengthCM` along it. All inputs/outputs in
// the same units (UE centimetres when used with NetworkRibbon data).
//
// UE4 convention (verified empirically on TSW ribbons):
//   - Tangent vector points in the direction of the ribbon's Length axis.
//   - Radius may be 0/∞ for straight segments (treat any non-positive or
//     non-finite value as straight).
//   - Perpendicular to the curve centre from start is (+ty, -tx). Sweep angle
//     = -length / radius (negative — clockwise when viewed from above).
func ArcDelta(startX, startY, tangentX, tangentY, radius, length float64) (dx, dy float64) {
	tn := math.Hypot(tangentX, tangentY)
	if tn == 0 {
		return 0, 0
	}
	tx := tangentX / tn
	ty := tangentY / tn
	if radius == 0 || math.IsInf(radius, 0) || math.IsNaN(radius) {
		return length * tx, length * ty
	}
	cx := startX + radius*ty
	cy := startY - radius*tx
	sweep := -length / radius
	cosS := math.Cos(sweep)
	sinS := math.Sin(sweep)
	sdx := startX - cx
	sdy := startY - cy
	ex := cx + cosS*sdx - sinS*sdy
	ey := cy + sinS*sdx + cosS*sdy
	return ex - startX, ey - startY
}

// ArcPoint returns the absolute (x, y) of a point at arc-distance `atLen`
// along a segment starting at (startX, startY) with the given tangent &
// radius. Equivalent to start + ArcDelta(..., atLen).
func ArcPoint(startX, startY, tangentX, tangentY, radius, atLen float64) (x, y float64) {
	dx, dy := ArcDelta(startX, startY, tangentX, tangentY, radius, atLen)
	return startX + dx, startY + dy
}
