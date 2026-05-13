// scene_xform.go — read USceneComponent transforms (RelativeLocation,
// RelativeRotation, AttachParent) from any export, and walk the AttachParent
// chain so component-local coordinates can be lifted to tile-local space.
//
// Why: SplineMeshComponent.SplineParams is in *component-local* space.
// Each spline mesh sits inside an actor whose RootComponent (a SceneComponent
// like DefaultSceneRoot) carries the actor's tile-local position. To draw
// the visible track at the right spot we need the cumulative transform from
// the spline mesh's frame all the way up to the actor's root.
//
// We only handle yaw rotation (Z-axis) — pitch and roll are essentially
// always zero on flat-rail track meshes. If a future tile breaks this
// assumption we'll see segments tilted in 2D and add the missing axes.
package uasset

import "math"

// SceneXform is the local-frame transform of one SceneComponent — i.e. its
// position and yaw RELATIVE TO ITS ATTACHPARENT. Defaults are zero so an
// export that doesn't override these properties (because they match the
// class default) parses cleanly without explicit handling.
//
// AttachParent is an FPackageIndex (positive = export idx, 0 = no parent /
// this component IS the actor's root). Negative values (imports) shouldn't
// happen for in-tile attachments and we treat them as "no parent."
type SceneXform struct {
	LocX, LocY, LocZ float64
	YawDeg           float64 // pitch+roll ignored; see file comment
	AttachParent     int32
}

// ReadSceneXform reads RelativeLocation/RelativeRotation/AttachParent from
// the export at idx (1-based, FPackageIndex convention). Out-of-range or
// non-export idx returns the zero SceneXform.
func (u *Umap) ReadSceneXform(idx int) SceneXform {
	var x SceneXform
	if idx <= 0 || idx > len(u.Exports) {
		return x
	}
	e := u.Exports[idx-1]
	pr := u.PropertyReader(e)
	if pr == nil {
		return x
	}
	for pr.remaining() > 8 {
		t, ok := pr.readTag()
		if !ok {
			break
		}
		dp := pr.p
		switch {
		case t.name == "RelativeLocation" && t.ptype == "StructProperty":
			x.LocX = float64(pr.f32())
			x.LocY = float64(pr.f32())
			x.LocZ = float64(pr.f32())
		case t.name == "RelativeRotation" && t.ptype == "StructProperty":
			pr.f32() // pitch (ignored)
			x.YawDeg = float64(pr.f32())
			pr.f32() // roll (ignored)
		case t.name == "AttachParent" && t.ptype == "ObjectProperty":
			x.AttachParent = pr.i32()
		}
		pr.seek(dp + t.size)
	}
	return x
}

// WorldXform walks the AttachParent chain rooted at idx (a positive export
// index, FPackageIndex form), accumulating local SceneXform transforms,
// and returns the cumulative tile-local position+yaw.
//
// Composition rule: walking from the deepest attached child up to the root,
// we end up with a chain [child, ..., root]. To compose correctly we apply
// the chain in *reverse* order (root → child), at each step rotating the
// link's local position by the running yaw before adding to the running
// translation. This is standard transform-of-transforms.
func (u *Umap) WorldXform(idx int) (x, y, yaw float64) {
	visited := map[int32]bool{}
	var chain []SceneXform
	cur := int32(idx)
	for cur > 0 && !visited[cur] && len(chain) < 32 {
		visited[cur] = true
		sx := u.ReadSceneXform(int(cur))
		chain = append(chain, sx)
		cur = sx.AttachParent
	}
	for i := len(chain) - 1; i >= 0; i-- {
		link := chain[i]
		rad := yaw * math.Pi / 180
		cy, sn := math.Cos(rad), math.Sin(rad)
		x += cy*link.LocX - sn*link.LocY
		y += sn*link.LocX + cy*link.LocY
		yaw += link.YawDeg
	}
	return
}

// rotate2D rotates (x,y) by yawDeg around the origin and returns the result.
// Exported because spline_mesh.go and the CLI both need it.
func rotate2D(x, y, yawDeg float64) (rx, ry float64) {
	rad := yawDeg * math.Pi / 180
	cy, sn := math.Cos(rad), math.Sin(rad)
	return cy*x - sn*y, sn*x + cy*y
}
