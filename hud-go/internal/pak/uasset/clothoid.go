// clothoid.go — extract NetworkCurveClothoidSpiral parameters from TT tiles
// and link them to their owning NetworkRibbon via the ribbon's Curve
// ObjectProperty (an FPackageIndex pointing to the curve export).
//
// Why: tc-hermite currently approximates clothoid ribbons with cubic
// Hermite using start_pos/start_tan from the ribbon and end_pos/end_tan
// inferred from the next ribbon in the walker chain. That's both lossy
// (Hermite isn't a Fresnel integral) and walker-dependent (a wrong
// chaining decision puts the clothoid's end at the wrong place).
//
// Real clothoid math, given the parameter A:
//   curvature(s) = s / A²
//   heading(s)   = θ₀ + s² / (2 A²)
//   position(s)  = StartPos + ∫₀ˢ (cos(heading), sin(heading)) ds'
//
// A single number A fully describes the clothoid. The PowersOfA array
// stored in the asset is precomputed A^1..A^9 — used by the engine's
// Taylor-series evaluator at runtime; we only need PowersOfA[0] (=A).
//
// Sign of A determines the curve direction (left vs right).
package uasset

import (
	"math"
	"strings"
)

// ClothoidParams describes one clothoid completely.
//
// All position/length values are in centimetres (UE world units).
// StartTangent2D is a unit vector (sin/cos of the start heading).
// RadialOffset offsets the curve laterally from its base; usually 0.
type ClothoidParams struct {
	A              float64 // = PowersOfA[0]; clothoid parameter (signed)
	L              float64 // = ClothoidLength
	RadialOffset   float64
	StartX, StartY float64 // = StartPosition2D
	StartTanX      float64
	StartTanY      float64 // = StartTangent2D (unit)
}

// ParseClothoidsByRibbonGUID walks every export in a TT tile and returns a
// map of ribbon-GUID → ClothoidParams, populated only for ribbons whose
// Curve points to a NetworkCurveClothoidSpiral export.
//
// We do this in two passes:
//
//	1. Index every ClothoidSpiral by its 1-based export index (FPackageIndex
//	   form), pulling the curve params.
//	2. Walk every NetworkRibbon, read its Curve FPackageIndex and RibbonGuid,
//	   and link them via the index map.
//
// Ribbons whose Curve points to a NetworkCurveCircularArc (or any non-
// clothoid) are simply absent from the result.
func ParseClothoidsByRibbonGUID(uassetPath string) (map[string]ClothoidParams, error) {
	u, err := ReadUmap(uassetPath)
	if err != nil {
		return nil, err
	}

	// Pass 1: index clothoid spirals by export idx (1-based, FPackageIndex form).
	clothoidByIdx := map[int32]ClothoidParams{}
	for i, e := range u.Exports {
		if !isClothoidSpiral(e.ObjectName) {
			continue
		}
		pr := u.PropertyReader(e)
		if pr == nil {
			continue
		}
		p, ok := walkClothoidSpiral(pr)
		if !ok {
			continue
		}
		clothoidByIdx[int32(i+1)] = p
	}

	// Pass 2: walk ribbons, link via Curve FPackageIndex.
	out := map[string]ClothoidParams{}
	for _, e := range u.Exports {
		if !isNetworkRibbonExport(e.ObjectName) {
			continue
		}
		pr := u.PropertyReader(e)
		if pr == nil {
			continue
		}
		guid, curveIdx := walkRibbonForGuidAndCurve(pr)
		if guid == "" || curveIdx <= 0 {
			continue
		}
		if p, ok := clothoidByIdx[curveIdx]; ok {
			out[guid] = p
		}
	}
	return out, nil
}

func isClothoidSpiral(name string) bool {
	if strings.HasPrefix(name, "Default__") {
		return false
	}
	return strings.Contains(name, "NetworkCurveClothoidSpiral")
}

func isNetworkRibbonExport(name string) bool {
	if strings.HasPrefix(name, "Default__") {
		return false
	}
	// Must be a NetworkRibbon proper, not a NetworkRibbonRange or similar.
	// "NetworkRibbon_N" pattern is what we want.
	return strings.HasPrefix(name, "NetworkRibbon_") || name == "NetworkRibbon"
}

// walkRibbonForGuidAndCurve reads a NetworkRibbon's property stream and
// returns its RibbonGuid (normalised) and Curve FPackageIndex.
func walkRibbonForGuidAndCurve(r *reader) (guid string, curveIdx int32) {
	for r.remaining() > 8 {
		t, ok := r.readTag()
		if !ok {
			break
		}
		dp := r.p
		switch {
		case t.name == "RibbonGuid" && t.ptype == "StructProperty" && t.structType == "Guid":
			var raw [16]byte
			copy(raw[:], r.d[dp:dp+16])
			guid = NormalizeGUID(fmtGUID(raw))
		case t.name == "Curve" && t.ptype == "ObjectProperty":
			// Body is a 4-byte int32 FPackageIndex.
			curveIdx = r.i32()
		}
		r.seek(dp + t.size)
	}
	return
}

// walkClothoidSpiral reads a NetworkCurveClothoidSpiral export and pulls
// the ClothoidParams. Returns ok=false if the Spiral struct wasn't found.
func walkClothoidSpiral(r *reader) (ClothoidParams, bool) {
	var p ClothoidParams
	found := false
	for r.remaining() > 8 {
		t, ok := r.readTag()
		if !ok {
			break
		}
		dp := r.p
		switch {
		case t.name == "Spiral" && t.ptype == "StructProperty" &&
			t.structType == "SpiralTransition":
			walkSpiralTransition(r, dp, t.size, &p)
			found = true
		case t.name == "StartPosition2D" && t.ptype == "StructProperty" &&
			t.structType == "Vector2D":
			p.StartX = float64(r.f32())
			p.StartY = float64(r.f32())
		case t.name == "StartTangent2D" && t.ptype == "StructProperty" &&
			t.structType == "Vector2D":
			p.StartTanX = float64(r.f32())
			p.StartTanY = float64(r.f32())
		case t.name == "Length" && t.ptype == "FloatProperty":
			// Top-level Length — same value as ClothoidLength when
			// no RadialOffset stretching is in play. We'll prefer
			// ClothoidLength from the inner Spiral if both are present.
			if p.L == 0 {
				p.L = float64(r.f32())
			}
		}
		r.seek(dp + t.size)
	}
	return p, found
}

// walkSpiralTransition reads the body of a SpiralTransition struct, looking
// for BaseClothoid (a Clothoid struct holding PowersOfA), ClothoidLength,
// and RadialOffset.
func walkSpiralTransition(r *reader, dp, size int, p *ClothoidParams) {
	end := dp + size
	for r.p < end {
		t, ok := r.readTag()
		if !ok {
			break
		}
		tdp := r.p
		switch {
		case t.name == "BaseClothoid" && t.ptype == "StructProperty" &&
			t.structType == "Clothoid":
			walkClothoid(r, tdp, t.size, p)
		case t.name == "ClothoidLength" && t.ptype == "DoubleProperty":
			p.L = readDouble(r)
		case t.name == "RadialOffset" && t.ptype == "DoubleProperty":
			p.RadialOffset = readDouble(r)
		}
		r.seek(tdp + t.size)
	}
	r.seek(end)
}

// walkClothoid reads a Clothoid struct body. Its only property of interest
// is PowersOfA — a static array stored as repeated DoubleProperty tags
// with the same name (UE serialises static arrays this way: each element
// is its own FPropertyTag with an arr_idx field that we currently skip in
// readTag). We capture only the first occurrence (= A^1 = A).
func walkClothoid(r *reader, dp, size int, p *ClothoidParams) {
	end := dp + size
	for r.p < end {
		t, ok := r.readTag()
		if !ok {
			break
		}
		tdp := r.p
		if t.name == "PowersOfA" && t.ptype == "DoubleProperty" && p.A == 0 {
			p.A = readDouble(r)
		}
		r.seek(tdp + t.size)
	}
	r.seek(end)
}

// readDouble reads an 8-byte little-endian float64 from the reader.
func readDouble(r *reader) float64 {
	bits := uint64(r.i64())
	return math.Float64frombits(bits)
}

// IntegrateClothoid samples a clothoid as a polyline of N+1 points covering
// arc length [0, L]. Returns slices of (X, Y) in cm — the same coordinate
// system as the ClothoidParams' StartX/StartY.
//
// Math:
//
//	heading(s) = atan2(StartTanY, StartTanX) + s² / (2 A²)
//	(x,y)(s) = StartPos + ∫₀ˢ (cos(heading), sin(heading)) ds'
//
// The integral has no closed form (it's a Fresnel integral). We use the
// trapezoidal rule with a fine step — accurate to micro-metres for typical
// track curvatures and well below any visible map error.
//
// RadialOffset shifts the entire curve perpendicular to its starting
// tangent (to the LEFT of travel for positive offset); we apply it as a
// constant lateral translation, since the engine appears to use it for
// gauge-symmetric pairs (rare in TC; usually 0).
func IntegrateClothoid(p ClothoidParams, n int) (xs, ys []float64) {
	if n < 2 {
		n = 2
	}
	startTheta := math.Atan2(p.StartTanY, p.StartTanX)
	xs = make([]float64, n+1)
	ys = make([]float64, n+1)
	xs[0] = p.StartX
	ys[0] = p.StartY
	if p.RadialOffset != 0 {
		// shift starting point perpendicular to tangent (left-positive)
		nx, ny := -p.StartTanY, p.StartTanX
		xs[0] += p.RadialOffset * nx
		ys[0] += p.RadialOffset * ny
	}
	a2 := p.A * p.A
	if a2 == 0 {
		return
	}
	ds := p.L / float64(n)
	// Trapezoidal: position[i] = position[i-1] + ds * mean(cos/sin at endpoints).
	prevCos := math.Cos(startTheta)
	prevSin := math.Sin(startTheta)
	for i := 1; i <= n; i++ {
		s := float64(i) * ds
		theta := startTheta + s*s/(2*a2)
		c, sg := math.Cos(theta), math.Sin(theta)
		xs[i] = xs[i-1] + ds*0.5*(prevCos+c)
		ys[i] = ys[i-1] + ds*0.5*(prevSin+sg)
		prevCos, prevSin = c, sg
	}
	return
}
