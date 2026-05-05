// ribbon_cooked.go — binary-walker extraction of NetworkRibbon + linked
// curve geometry from a single TT_*.umap. Produces the per-ribbon record
// our `ribbons-to-geojson` CSV expects.
//
// Why this exists: the editor-Python pipeline confirmed that
// `WorldLocation + StartPosition2D = world cm`. In cooked uassets the same
// world position is stored on the ribbon as `CachedStartPosition`
// (FarVector). Reading CachedStartPosition gives us the answer directly,
// no editor required and no per-tile coord-frame heuristics.
//
// We deliberately use the binary walker (not the JSON path through
// UAssetGUI) — the JSON tojson tool OOMs on big tile files; the binary
// walker handles them all in milliseconds.
package uasset

import (
	"encoding/binary"
	"math"
	"strings"
)

// CookedRibbon is one row of cooked-pak ribbon data, sufficient for
// `ribbons-to-geojson` to render the canonical centerline.
type CookedRibbon struct {
	TileName        string  // e.g. "TT_x-30_y-18" — for the actor_path column
	RibbonName      string  // e.g. "NetworkRibbon_42"
	RibbonGUID      string  // 32-char hex (lowercase normalised)
	StartNodeGUID   string
	EndNodeGUID     string
	CurveClass      string  // "NetworkCurveCircularArc" / "NetworkCurveClothoidSpiral"
	// World position of the curve's t=0 = WorldLocation + StartPosition2D.
	// Filled in by the parser before return.
	StartX, StartY  float64
	// Per-ribbon offset (cm). For ribbons authored in world coords this is
	// (0,0); for tile-local ones it's the explicit offset stored on the
	// ribbon. CachedStartPosition is preferred when present, else we use
	// WorldLocation. Either way StartX/StartY is already world cm.
	TangentX        float64
	TangentY        float64
	Length          float64
	Radius          float64 // 0 for straight or clothoid; >0 for curved arcs
	HasRadius       bool
}

// ParseCookedRibbonsFromUmap walks one .umap, returns one CookedRibbon per
// NetworkRibbon export, with curve geometry resolved via the linked curve
// sub-export. Tile name is taken from the file name (caller passes it in
// since the package layer doesn't know paths).
func ParseCookedRibbonsFromUmap(uassetPath, tileName string) ([]CookedRibbon, error) {
	u, err := ReadUmap(uassetPath)
	if err != nil {
		return nil, err
	}

	// Pass 1: walk all curves, store per-export-idx.
	type curveInfo struct {
		class            string
		sx, sy, tx, ty   float64
		length           float64
		radius           float64
		hasRadius        bool
	}
	curves := map[int32]curveInfo{}
	for i, e := range u.Exports {
		isArc := strings.Contains(e.ObjectName, "NetworkCurveCircularArc") &&
			!strings.HasPrefix(e.ObjectName, "Default__")
		isClothoid := isClothoidSpiral(e.ObjectName)
		if !isArc && !isClothoid {
			continue
		}
		pr := u.PropertyReader(e)
		if pr == nil {
			continue
		}
		ci := curveInfo{}
		if isArc {
			ci.class = "NetworkCurveCircularArc"
		} else {
			ci.class = "NetworkCurveClothoidSpiral"
		}
		walkCurveGeometry(pr, &ci.sx, &ci.sy, &ci.tx, &ci.ty,
			&ci.length, &ci.radius, &ci.hasRadius)
		curves[int32(i+1)] = ci
	}

	// Pass 2: walk ribbons, pull GUIDs + WorldLocation + CachedStartPosition + curve link.
	var out []CookedRibbon
	for _, e := range u.Exports {
		if !isNetworkRibbonExport(e.ObjectName) {
			continue
		}
		pr := u.PropertyReader(e)
		if pr == nil {
			continue
		}
		rb := CookedRibbon{
			TileName:   tileName,
			RibbonName: e.ObjectName,
		}
		var curveIdx int32
		var wlX, wlY float64
		var hasWL, hasCSP bool
		var cspX, cspY float64
		walkRibbonCookedFields(pr, &rb, &curveIdx, &wlX, &wlY, &hasWL, &cspX, &cspY, &hasCSP)
		if c, ok := curves[curveIdx]; ok {
			rb.CurveClass = c.class
			rb.TangentX, rb.TangentY = c.tx, c.ty
			rb.Length = c.length
			rb.Radius, rb.HasRadius = c.radius, c.hasRadius
		}
		// World position priority:
		//  1. WorldLocation + curve.StartPosition2D (editor-verified recipe)
		//  2. CachedStartPosition (might disagree by a hair if it's transient)
		//  3. curve.StartPosition2D alone (last resort, may be tile-local)
		c := curves[curveIdx]
		switch {
		case hasWL:
			rb.StartX = c.sx + wlX
			rb.StartY = c.sy + wlY
		case hasCSP:
			rb.StartX, rb.StartY = cspX, cspY
		default:
			rb.StartX, rb.StartY = c.sx, c.sy
		}
		out = append(out, rb)
	}
	return out, nil
}

// walkCurveGeometry reads a NetworkCurveCircularArc or NetworkCurveClothoidSpiral
// export's properties and pulls StartPosition2D, StartTangent2D, Length, Radius.
func walkCurveGeometry(r *reader, sx, sy, tx, ty, length, radius *float64, hasRadius *bool) {
	for r.remaining() > 8 {
		t, ok := r.readTag()
		if !ok {
			return
		}
		dp := r.p
		switch {
		case t.name == "StartPosition2D" && t.ptype == "StructProperty" &&
			t.structType == "Vector2D":
			*sx = float64(r.f32())
			*sy = float64(r.f32())
		case t.name == "StartTangent2D" && t.ptype == "StructProperty" &&
			t.structType == "Vector2D":
			*tx = float64(r.f32())
			*ty = float64(r.f32())
		case t.name == "Length" && t.ptype == "FloatProperty":
			*length = float64(r.f32())
		case t.name == "Radius" && t.ptype == "FloatProperty":
			*radius = float64(r.f32())
			*hasRadius = true
		}
		r.seek(dp + t.size)
	}
}

// walkRibbonCookedFields reads a NetworkRibbon's property stream and pulls
// the GUIDs, the Curve FPackageIndex, WorldLocation (IntVector or FVector2D),
// and CachedStartPosition (FarVector — 2 doubles + 1 float).
func walkRibbonCookedFields(r *reader, rb *CookedRibbon, curveIdx *int32,
	wlX, wlY *float64, hasWL *bool,
	cspX, cspY *float64, hasCSP *bool) {
	for r.remaining() > 8 {
		t, ok := r.readTag()
		if !ok {
			return
		}
		dp := r.p
		switch {
		case t.name == "RibbonGuid" && t.ptype == "StructProperty" &&
			t.structType == "Guid":
			var raw [16]byte
			copy(raw[:], r.d[dp:dp+16])
			rb.RibbonGUID = NormalizeGUID(fmtGUID(raw))
		case t.name == "StartNodeGuid" && t.ptype == "StructProperty" &&
			t.structType == "Guid":
			var raw [16]byte
			copy(raw[:], r.d[dp:dp+16])
			rb.StartNodeGUID = NormalizeGUID(fmtGUID(raw))
		case t.name == "EndNodeGuid" && t.ptype == "StructProperty" &&
			t.structType == "Guid":
			var raw [16]byte
			copy(raw[:], r.d[dp:dp+16])
			rb.EndNodeGUID = NormalizeGUID(fmtGUID(raw))
		case t.name == "Curve" && t.ptype == "ObjectProperty":
			*curveIdx = r.i32()
		case t.name == "WorldLocation" && t.ptype == "StructProperty":
			// Tagged sub-struct: walk inner property stream for X / Y.
			// IntVector serialises X, Y, Z each as IntProperty; FVector as
			// FloatProperty; FarVector as DoubleProperty. We read whichever
			// is there.
			x, y, ok := readTaggedXY(&reader{d: r.d[dp:dp+t.size], nm: r.nm})
			if ok {
				*wlX, *wlY, *hasWL = x, y, true
			}
		case t.name == "CachedStartPosition" && t.ptype == "StructProperty":
			// Tagged FarVector: inner X (Double), Y (Double), Z (Float).
			x, y, ok := readTaggedXY(&reader{d: r.d[dp:dp+t.size], nm: r.nm})
			if ok {
				*cspX, *cspY, *hasCSP = x, y, true
			}
		}
		r.seek(dp + t.size)
	}
}

// readTaggedXY walks a tagged sub-struct property stream and pulls the X
// and Y named scalars (Int, Float, or Double). Returns ok=true if both are
// found. Used for WorldLocation (IntVector or FVector) and
// CachedStartPosition (FarVector).
//
// Defensive: structs with unusual body sizes can lead the inner readTag to
// run off the end. We catch the panic and return ok=false so the caller
// falls through to its other-source priorities (CSP/StartPosition2D).
func readTaggedXY(r *reader) (x, y float64, ok bool) {
	defer func() {
		if rec := recover(); rec != nil {
			ok = false
		}
	}()
	gotX, gotY := false, false
	for r.remaining() >= 24 && !(gotX && gotY) {
		t, more := r.readTag()
		if !more {
			break
		}
		dp := r.p
		switch t.name {
		case "X":
			switch t.ptype {
			case "IntProperty":
				x = float64(int32(binary.LittleEndian.Uint32(r.d[dp:])))
				gotX = true
			case "FloatProperty":
				x = float64(math.Float32frombits(binary.LittleEndian.Uint32(r.d[dp:])))
				gotX = true
			case "DoubleProperty":
				x = readF64(r.d[dp:])
				gotX = true
			}
		case "Y":
			switch t.ptype {
			case "IntProperty":
				y = float64(int32(binary.LittleEndian.Uint32(r.d[dp:])))
				gotY = true
			case "FloatProperty":
				y = float64(math.Float32frombits(binary.LittleEndian.Uint32(r.d[dp:])))
				gotY = true
			case "DoubleProperty":
				y = readF64(r.d[dp:])
				gotY = true
			}
		}
		r.seek(dp + t.size)
	}
	return x, y, gotX && gotY
}

func readF64(b []byte) float64 {
	if len(b) < 8 {
		return 0
	}
	return math.Float64frombits(binary.LittleEndian.Uint64(b))
}
