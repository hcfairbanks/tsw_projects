// spline_mesh.go — extract SplineMeshComponent's SplineParams from a umap.
//
// Why: SplineMeshComponent is the engine's primitive for "render this static
// mesh bent along a cubic curve from StartPos to EndPos with explicit
// StartTangent and EndTangent." For TSW6 routes, the visible rail bed (and
// rails, ties, ballast) is rendered as thousands of these — one per visible
// track segment. The (StartPos, StartTangent, EndPos, EndTangent) tuples are
// the engine's *exact* track centerline as drawn on screen, with no
// derivation, no tangent inference, and no walker chaining.
//
// If you sample these via cubic Hermite at fine resolution and project to
// lat/lng, you get the rendered track as you see it in-game — including
// sub-meter parallel-track separation that the NetworkRibbon math can't
// reproduce because clothoid end-tangents have to be inferred from neighbours.
//
// We deliberately read SplineParams in component-local coords. World-space
// conversion (combining the owner actor's transform) is the caller's job —
// trying to do it inside the parser would require resolving FObjectImport
// references and that's a bigger fight than we need today.
package uasset

import (
	"math"
	"strings"
)

// SplineMeshSegment is one cubic Hermite curve as carried by a single
// SplineMeshComponent's SplineParams. All coordinates are in component-
// local space (centimetres, UE convention: X+ forward, Y+ right, Z+ up).
//
// CompName / OwnerExportIdx help the caller correlate back to the parent
// actor for transform composition or class filtering ("is this static mesh
// a track rail or something else?").
type SplineMeshSegment struct {
	CompName       string // FName of the SplineMeshComponent export
	OwnerExportIdx int32  // index into Umap.Exports of the parent actor (0 if unknown)

	// SplineParams in component-local space (centimetres).
	StartX, StartY, StartZ          float64
	StartTanX, StartTanY, StartTanZ float64
	EndX, EndY, EndZ                float64
	EndTanX, EndTanY, EndTanZ       float64
	StartRoll, EndRoll              float64
	StartScaleX, StartScaleY        float64
	EndScaleX, EndScaleY            float64
	ForwardAxis                     string // "X", "Y", or "Z" — almost always X for tracks

	// Inherited USceneComponent transform — used to lift the local-space
	// SplineParams into the parent actor's space. Defaults are zero vectors
	// because UE only serialises non-default property values.
	RelLocX, RelLocY, RelLocZ          float64
	RelRotPitch, RelRotYaw, RelRotRoll float64 // degrees
	RelScaleX, RelScaleY, RelScaleZ    float64 // default 1

	// Tile-local frame of the spline mesh's coordinate system. Computed by
	// walking AttachParent up to the actor root and accumulating
	// RelativeLocation/Rotation. Apply this to SplineParams to get
	// tile-local positions; add the tile origin for world coordinates.
	AnchorX, AnchorY, AnchorYawDeg float64
}

// ParseSplineMeshesFromUmap finds every SplineMeshComponent export and
// returns its SplineParams *lifted into tile-local space* — that is,
// composed with the cumulative AttachParent-chain transform of the parent
// actor. Without this composition the SplineParams alone are useless: each
// component reports positions in a frame attached to its parent, and the
// parent (typically DefaultSceneRoot) holds the actor's tile-local position.
//
// We exclude Default__* class templates.
func ParseSplineMeshesFromUmap(uassetPath string) ([]SplineMeshSegment, error) {
	u, err := ReadUmap(uassetPath)
	if err != nil {
		return nil, err
	}
	var segs []SplineMeshSegment
	for i, e := range u.Exports {
		if !isSplineMeshComponent(e.ObjectName) {
			continue
		}
		pr := u.PropertyReader(e)
		if pr == nil {
			continue
		}
		s, ok := walkSplineMeshComponent(pr)
		if !ok {
			continue
		}
		s.CompName = e.ObjectName
		s.OwnerExportIdx = int32(i + 1) // FPackageIndex form

		// Walk the AttachParent chain to get the cumulative tile-local
		// transform of THIS spline-mesh's frame. Note that the spline mesh
		// itself sits at i+1 in FPackageIndex form, and its own
		// RelativeLocation/Rotation may already have been read above; but
		// WorldXform reads them again from the property stream by index, so
		// they're not double-counted.
		s.AnchorX, s.AnchorY, s.AnchorYawDeg = u.WorldXform(i + 1)
		segs = append(segs, s)
	}
	return segs, nil
}

func isSplineMeshComponent(name string) bool {
	if strings.HasPrefix(name, "Default__") {
		return false
	}
	return strings.Contains(name, "SplineMeshComponent")
}

// walkSplineMeshComponent reads the property stream of one SplineMesh export
// and pulls SplineParams + ForwardAxis + the inherited SceneComponent
// transform. Returns ok=false if SplineParams wasn't found.
func walkSplineMeshComponent(r *reader) (SplineMeshSegment, bool) {
	s := SplineMeshSegment{RelScaleX: 1, RelScaleY: 1, RelScaleZ: 1}
	gotParams := false
	for r.remaining() > 8 {
		t, ok := r.readTag()
		if !ok {
			break
		}
		dp := r.p
		switch {
		case t.name == "SplineParams" && t.ptype == "StructProperty" &&
			t.structType == "SplineMeshParams":
			readSplineMeshParams(r, dp, t.size, &s)
			gotParams = true
		case t.name == "ForwardAxis" && t.ptype == "ByteProperty":
			v := r.fname()
			s.ForwardAxis = enumLastSegment(v)
		case t.name == "RelativeLocation" && t.ptype == "StructProperty":
			s.RelLocX = float64(r.f32())
			s.RelLocY = float64(r.f32())
			s.RelLocZ = float64(r.f32())
		case t.name == "RelativeRotation" && t.ptype == "StructProperty":
			// Rotator order: Pitch, Yaw, Roll (each float, degrees).
			s.RelRotPitch = float64(r.f32())
			s.RelRotYaw = float64(r.f32())
			s.RelRotRoll = float64(r.f32())
		case t.name == "RelativeScale3D" && t.ptype == "StructProperty":
			s.RelScaleX = float64(r.f32())
			s.RelScaleY = float64(r.f32())
			s.RelScaleZ = float64(r.f32())
		}
		r.seek(dp + t.size)
	}
	return s, gotParams
}

// TileLocalEnds returns the segment's StartPos/StartTangent/EndPos/EndTangent
// in tile-local space (cm) — i.e. composed with the AttachParent-chain
// transform recorded during parsing.
//
// The composition is: rotate the SplineParams positions by Anchor.YawDeg,
// then translate by Anchor.{X,Y}. Tangents are direction vectors so they
// rotate but don't translate.
//
// Z is preserved for sanity-checking elevation but the 2D projection only
// uses X/Y.
func (s SplineMeshSegment) TileLocalEnds() (sx, sy, sz, stx, sty, stz, ex, ey, ez, etx, ety, etz float64) {
	rsx, rsy := rotate2D(s.StartX, s.StartY, s.AnchorYawDeg)
	sx = rsx + s.AnchorX
	sy = rsy + s.AnchorY
	sz = s.StartZ
	stx, sty = rotate2D(s.StartTanX, s.StartTanY, s.AnchorYawDeg)
	stz = s.StartTanZ
	rex, rey := rotate2D(s.EndX, s.EndY, s.AnchorYawDeg)
	ex = rex + s.AnchorX
	ey = rey + s.AnchorY
	ez = s.EndZ
	etx, ety = rotate2D(s.EndTanX, s.EndTanY, s.AnchorYawDeg)
	etz = s.EndTanZ
	return
}

// readSplineMeshParams parses a SplineMeshParams struct body. Layout (UE 4.27):
//
//   StartPos      : Vector
//   StartTangent  : Vector
//   StartScale    : Vector2D
//   StartRoll     : float
//   StartOffset   : Vector2D
//   EndPos        : Vector
//   EndTangent    : Vector
//   EndScale      : Vector2D
//   EndRoll       : float
//   EndOffset     : Vector2D
//
// Each is its own tagged property (it's a property stream, not an atomic
// blob). We pick out what we need and ignore the rest.
func readSplineMeshParams(r *reader, dp, size int, s *SplineMeshSegment) {
	end := dp + size
	for r.p < end {
		t, ok := r.readTag()
		if !ok {
			break
		}
		tdp := r.p
		switch t.name {
		case "StartPos":
			s.StartX = float64(r.f32())
			s.StartY = float64(r.f32())
			s.StartZ = float64(r.f32())
		case "StartTangent":
			s.StartTanX = float64(r.f32())
			s.StartTanY = float64(r.f32())
			s.StartTanZ = float64(r.f32())
		case "EndPos":
			s.EndX = float64(r.f32())
			s.EndY = float64(r.f32())
			s.EndZ = float64(r.f32())
		case "EndTangent":
			s.EndTanX = float64(r.f32())
			s.EndTanY = float64(r.f32())
			s.EndTanZ = float64(r.f32())
		case "StartScale":
			s.StartScaleX = float64(r.f32())
			s.StartScaleY = float64(r.f32())
		case "EndScale":
			s.EndScaleX = float64(r.f32())
			s.EndScaleY = float64(r.f32())
		case "StartRoll":
			s.StartRoll = float64(r.f32())
		case "EndRoll":
			s.EndRoll = float64(r.f32())
		}
		r.seek(tdp + t.size)
	}
	r.seek(end)
}

// enumLastSegment strips a "Foo::Bar" enum name down to "Bar".
func enumLastSegment(s string) string {
	if i := lastIndexOfDoubleColon(s); i >= 0 {
		return s[i+2:]
	}
	return s
}

func lastIndexOfDoubleColon(s string) int {
	for i := len(s) - 2; i >= 0; i-- {
		if s[i] == ':' && s[i+1] == ':' {
			return i
		}
	}
	return -1
}

// HermiteSample evaluates a cubic Hermite curve for a single component
// (X, Y, or Z) at parameter t in [0,1]. Useful for sampling SplineMesh
// segments at fixed resolution.
func HermiteSample(p0, m0, p1, m1, t float64) float64 {
	t2 := t * t
	t3 := t2 * t
	h00 := 2*t3 - 3*t2 + 1
	h10 := t3 - 2*t2 + t
	h01 := -2*t3 + 3*t2
	h11 := t3 - t2
	return h00*p0 + h10*m0 + h01*p1 + h11*m1
}

// HermiteLength approximates the chord-arc length of a Hermite segment by
// a Gauss-Legendre 4-point rule (good to ~0.1% for typical track curvatures).
func HermiteLength(sx, sy, sz, stx, sty, stz, ex, ey, ez, etx, ety, etz float64) float64 {
	// Derivatives of cubic Hermite are quadratic in t.
	deriv := func(p0, m0, p1, m1, t float64) float64 {
		t2 := t * t
		return (6*t2-6*t)*p0 + (3*t2-4*t+1)*m0 + (-6*t2+6*t)*p1 + (3*t2-2*t)*m1
	}
	// 4-point nodes/weights mapped to [0,1].
	nodes := [4]float64{0.069431844, 0.330009478, 0.669990522, 0.930568156}
	wts := [4]float64{0.173927423, 0.326072577, 0.326072577, 0.173927423}
	var sum float64
	for i, t := range nodes {
		dx := deriv(sx, stx, ex, etx, t)
		dy := deriv(sy, sty, ey, ety, t)
		dz := deriv(sz, stz, ez, etz, t)
		sum += wts[i] * math.Sqrt(dx*dx+dy*dy+dz*dz)
	}
	return sum
}

