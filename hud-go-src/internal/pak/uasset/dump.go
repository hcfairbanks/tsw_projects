// dump.go — recursive property-tree printer for diagnosis.
//
// Given an Umap and an export, walk the property stream and print a
// human-readable tree. We use the same readTag-based walker as the rest of
// the parser, but instead of looking for specific properties, we visit
// everything and print what we see.
//
// Goal: reconnaissance for "what data lives inside <ExportClass>?" — used
// to decide whether to invest in a full extractor for a new feature class.
//
// Intentionally lossy:
//   - For primitive properties we print the value or a one-line summary.
//   - For Struct properties we recurse, indented.
//   - For Array properties we print the count and the first N elements.
//   - For unknown types we print the type and skip the body — we never
//     mis-decode by guessing.
//
// Intentionally NOT a JSON dumper. We print to stderr as a tree because
// the goal is human-eyeball diagnosis, not machine consumption.
package uasset

import (
	"fmt"
	"io"
	"math"
	"strings"
)

// DumpExport prints the full property tree of one export to w. arrayMax
// caps how many elements of any one array are shown (others are summarised
// as "... +N more"). depth is the current indent level (0 for top-level).
func (u *Umap) DumpExport(w io.Writer, e ExportEntry, arrayMax int) {
	pr := u.PropertyReader(e)
	if pr == nil {
		fmt.Fprintf(w, "(no .uexp data for %s)\n", e.ObjectName)
		return
	}
	fmt.Fprintf(w, "Export %s (size=%d)\n", e.ObjectName, e.SerialSize)
	dumpPropertyStreamWithUmap(w, u, pr, 1, arrayMax, pr.remaining())
}

// resolveFPackageIndex turns an FPackageIndex into a human-readable label.
// Positive = export at idx-1; negative = import (we don't read imports here).
func (u *Umap) resolveFPackageIndex(idx int32) string {
	switch {
	case idx == 0:
		return "(null)"
	case idx > 0 && int(idx) <= len(u.Exports):
		return u.Exports[idx-1].ObjectName
	case idx < 0:
		return fmt.Sprintf("import[%d]", -idx-1)
	}
	return fmt.Sprintf("idx=%d", idx)
}

// dumpPropertyStreamWithUmap is dumpPropertyStream but with access to the
// Umap — letting ObjectProperty bodies resolve to the target export name
// instead of just printing "(size=4)".
func dumpPropertyStreamWithUmap(w io.Writer, u *Umap, r *reader, depth, arrayMax, bound int) {
	end := r.p + bound
	for r.p < end && r.remaining() > 0 {
		t, ok := r.readTag()
		if !ok {
			return
		}
		dp := r.p
		dumpOnePropertyWithUmap(w, u, r, t, depth, arrayMax)
		if r.p != dp+t.size {
			r.seek(dp + t.size)
		}
	}
}

// dumpOnePropertyWithUmap is dumpOneProperty with ObjectProperty resolution.
func dumpOnePropertyWithUmap(w io.Writer, u *Umap, r *reader, t tagInfo, depth, arrayMax int) {
	indent := strings.Repeat("  ", depth)
	if t.ptype == "ObjectProperty" || t.ptype == "AssetObjectProperty" {
		idx := r.i32()
		fmt.Fprintf(w, "%s%s : %s -> %s\n", indent, t.name, t.ptype, u.resolveFPackageIndex(idx))
		return
	}
	if t.ptype == "ArrayProperty" && t.innerType == "ObjectProperty" {
		// Array of FPackageIndex — print resolved targets.
		startP := r.p
		count := int(r.i32())
		fmt.Fprintf(w, "%s%s : Array<Object>[%d] {\n", indent, t.name, count)
		for i := 0; i < count && i < arrayMax; i++ {
			if r.p+4 > startP+t.size {
				break
			}
			idx := r.i32()
			fmt.Fprintf(w, "%s  [%d] -> %s\n", indent, i, u.resolveFPackageIndex(idx))
		}
		if count > arrayMax {
			fmt.Fprintf(w, "%s  ... +%d more\n", indent, count-arrayMax)
		}
		fmt.Fprintf(w, "%s}\n", indent)
		return
	}
	dumpOneProperty(w, r, t, depth, arrayMax)
}

// dumpPropertyStream walks tagged properties up to `bound` bytes from the
// reader's current position, or until None.
func dumpPropertyStream(w io.Writer, r *reader, depth, arrayMax, bound int) {
	end := r.p + bound
	for r.p < end && r.remaining() > 0 {
		t, ok := r.readTag()
		if !ok {
			return
		}
		dp := r.p
		dumpOneProperty(w, r, t, depth, arrayMax)
		// Always realign to the size in the tag — even if our handler
		// consumed fewer or more bytes.
		if r.p != dp+t.size {
			r.seek(dp + t.size)
		}
	}
}

func dumpOneProperty(w io.Writer, r *reader, t tagInfo, depth, arrayMax int) {
	indent := strings.Repeat("  ", depth)
	switch t.ptype {
	case "BoolProperty":
		fmt.Fprintf(w, "%s%s : Bool = %v\n", indent, t.name, t.boolVal != 0)
	case "ByteProperty", "EnumProperty":
		v := r.fname()
		fmt.Fprintf(w, "%s%s : %s(%s) = %s\n", indent, t.name, t.ptype, t.innerType, v)
	case "IntProperty":
		fmt.Fprintf(w, "%s%s : Int = %d\n", indent, t.name, r.i32())
	case "Int64Property":
		fmt.Fprintf(w, "%s%s : Int64 = %d\n", indent, t.name, r.i64())
	case "FloatProperty":
		fmt.Fprintf(w, "%s%s : Float = %f\n", indent, t.name, r.f32())
	case "DoubleProperty":
		// 8-byte double, read as int64 and reinterpret.
		bits := uint64(r.i64())
		fmt.Fprintf(w, "%s%s : Double = %f\n", indent, t.name, math.Float64frombits(bits))
	case "StrProperty", "NameProperty":
		var v string
		if t.ptype == "NameProperty" {
			v = r.fname()
		} else {
			v = r.fstr()
		}
		fmt.Fprintf(w, "%s%s : %s = %q\n", indent, t.name, t.ptype, v)
	case "TextProperty":
		v := r.ftext(t.size)
		fmt.Fprintf(w, "%s%s : Text = %q\n", indent, t.name, v)
	case "ObjectProperty", "SoftObjectProperty", "AssetObjectProperty":
		// ObjectProperty body is a 4-byte FPackageIndex (positive=export,
		// negative=import). SoftObjectProperty is an FString package name +
		// extra bits. We just note the type for now.
		fmt.Fprintf(w, "%s%s : %s (size=%d)\n", indent, t.name, t.ptype, t.size)
	case "StructProperty":
		fmt.Fprintf(w, "%s%s : Struct<%s> {\n", indent, t.name, t.structType)
		dumpStructBody(w, r, t, depth+1, arrayMax)
		fmt.Fprintf(w, "%s}\n", indent)
	case "ArrayProperty":
		dumpArrayProperty(w, r, t, depth, arrayMax)
	case "MapProperty":
		dumpMapProperty(w, r, t, depth, arrayMax)
	default:
		fmt.Fprintf(w, "%s%s : %s (skipped, size=%d)\n", indent, t.name, t.ptype, t.size)
	}
}

// dumpStructBody handles the body of a StructProperty. Some struct types are
// "atomic" (the body is fixed-format, not a property stream): Vector,
// Rotator, Quat, Guid, etc. Most others are property streams.
func dumpStructBody(w io.Writer, r *reader, t tagInfo, depth, arrayMax int) {
	indent := strings.Repeat("  ", depth)
	switch t.structType {
	case "Guid":
		var raw [16]byte
		copy(raw[:], r.d[r.p:r.p+16])
		fmt.Fprintf(w, "%s(Guid) %s\n", indent, NormalizeGUID(fmtGUID(raw)))
	case "Vector":
		x, y, z := r.f32(), r.f32(), r.f32()
		fmt.Fprintf(w, "%s(Vector) X=%f Y=%f Z=%f\n", indent, x, y, z)
	case "Vector2D":
		x, y := r.f32(), r.f32()
		fmt.Fprintf(w, "%s(Vec2) X=%f Y=%f\n", indent, x, y)
	case "Vector4", "Quat":
		x, y, z, ww := r.f32(), r.f32(), r.f32(), r.f32()
		fmt.Fprintf(w, "%s(%s) %f %f %f %f\n", indent, t.structType, x, y, z, ww)
	case "Rotator":
		p, y, ro := r.f32(), r.f32(), r.f32()
		fmt.Fprintf(w, "%s(Rotator) Pitch=%f Yaw=%f Roll=%f\n", indent, p, y, ro)
	case "LinearColor":
		rr, g, b, a := r.f32(), r.f32(), r.f32(), r.f32()
		fmt.Fprintf(w, "%s(Color) R=%f G=%f B=%f A=%f\n", indent, rr, g, b, a)
	case "Color":
		fmt.Fprintf(w, "%s(BGRA) %02x %02x %02x %02x\n",
			indent, r.u8(), r.u8(), r.u8(), r.u8())
	case "IntPoint":
		fmt.Fprintf(w, "%s(IntPoint) X=%d Y=%d\n", indent, r.i32(), r.i32())
	case "Box":
		// Min(Vector) Max(Vector) IsValid(uint8)
		ax, ay, az := r.f32(), r.f32(), r.f32()
		bx, by, bz := r.f32(), r.f32(), r.f32()
		valid := r.u8()
		fmt.Fprintf(w, "%s(Box) Min=%f,%f,%f Max=%f,%f,%f valid=%d\n",
			indent, ax, ay, az, bx, by, bz, valid)
	default:
		// Treat as property stream until None.
		dumpPropertyStream(w, r, depth, arrayMax, t.size)
	}
}

// dumpArrayProperty: format is i32 count + (for StructProperty inner) an
// inner FPropertyTag, then count element bodies.
func dumpArrayProperty(w io.Writer, r *reader, t tagInfo, depth, arrayMax int) {
	indent := strings.Repeat("  ", depth)
	startP := r.p
	endP := r.p + t.size
	count := int(r.i32())
	if count == 0 {
		fmt.Fprintf(w, "%s%s : Array<%s>[] (empty)\n", indent, t.name, t.innerType)
		return
	}
	if t.innerType != "StructProperty" {
		// For primitive arrays we just print count; reading every primitive
		// would explode the output and we rarely need it.
		fmt.Fprintf(w, "%s%s : Array<%s>[%d] (primitive — bytes %d..%d)\n",
			indent, t.name, t.innerType, count, startP, endP)
		return
	}
	innerTag, ok := r.readTag()
	if !ok {
		fmt.Fprintf(w, "%s%s : Array<Struct>[%d] (no inner tag — possibly malformed)\n",
			indent, t.name, count)
		return
	}
	fmt.Fprintf(w, "%s%s : Array<Struct=%s>[%d] {\n",
		indent, t.name, innerTag.structType, count)
	bodyEnd := r.p + innerTag.size
	for i := 0; i < count && r.p < bodyEnd; i++ {
		if i >= arrayMax {
			fmt.Fprintf(w, "%s  ... +%d more elements\n",
				indent, count-arrayMax)
			break
		}
		fmt.Fprintf(w, "%s  [%d] {\n", indent, i)
		startElem := r.p
		// Same atomic-vs-stream dispatch as dumpStructBody.
		dumpAtomicOrStream(w, r, innerTag.structType, depth+2, arrayMax, bodyEnd-startElem)
		fmt.Fprintf(w, "%s  }\n", indent)
		// If element walker didn't advance, bail out to avoid an infinite loop.
		if r.p == startElem {
			break
		}
	}
	fmt.Fprintf(w, "%s}\n", indent)
}

// dumpMapProperty walks a TMap<K,V> body.
//
// UE serialised layout after the tag header:
//
//	4 bytes : NumKeysToRemove (int32) — usually 0
//	4 bytes : NumEntries     (int32)
//	for each entry: KEY-bytes immediately followed by VALUE-bytes
//
// KEY and VALUE are *not* tagged; their encoding is determined by the
// key/value FName types captured in the tag header (t.mapKeyType,
// t.mapValueType). For unknown types we fall back to "skip the body" so
// we never mis-decode by guessing.
func dumpMapProperty(w io.Writer, r *reader, t tagInfo, depth, arrayMax int) {
	indent := strings.Repeat("  ", depth)
	startP := r.p
	endP := r.p + t.size
	r.skip(4) // NumKeysToRemove
	count := int(r.i32())
	if count == 0 {
		fmt.Fprintf(w, "%s%s : Map<%s,%s>[] (empty)\n",
			indent, t.name, t.mapKeyType, t.mapValueType)
		return
	}
	fmt.Fprintf(w, "%s%s : Map<%s,%s>[%d] {\n",
		indent, t.name, t.mapKeyType, t.mapValueType, count)
	defer fmt.Fprintf(w, "%s}\n", indent)
	for i := 0; i < count && r.p < endP; i++ {
		if i >= arrayMax {
			fmt.Fprintf(w, "%s  ... +%d more entries (bytes %d..%d remaining)\n",
				indent, count-arrayMax, r.p, endP)
			return
		}
		// KEY
		keyStr, keyOk := dumpMapKVOneSide(w, r, t.mapKeyType)
		if !keyOk {
			fmt.Fprintf(w, "%s  [%d] (unknown key type %q — bailing out at byte %d of %d)\n",
				indent, i, t.mapKeyType, r.p-startP, t.size)
			return
		}
		fmt.Fprintf(w, "%s  [%d] key=%s\n", indent, i, keyStr)
		// VALUE
		switch t.mapValueType {
		case "StructProperty":
			// Map struct values are written as a tagless property stream
			// terminated by None. Bound by the remaining map body so a
			// runaway can't read past the map.
			fmt.Fprintf(w, "%s    value: (struct stream)\n", indent)
			dumpPropertyStream(w, r, depth+3, arrayMax, endP-r.p)
		default:
			valStr, valOk := dumpMapKVOneSide(w, r, t.mapValueType)
			if !valOk {
				fmt.Fprintf(w, "%s    value: (unknown value type %q — bailing out)\n",
					indent, t.mapValueType)
				return
			}
			fmt.Fprintf(w, "%s    value=%s\n", indent, valStr)
		}
	}
}

// dumpMapKVOneSide decodes one side (key or value) of a map entry whose type
// is encoded by FName (NameProperty, IntProperty, StrProperty, ...). Returns
// the string form of the value plus ok=false if the type isn't handled.
func dumpMapKVOneSide(w io.Writer, r *reader, typ string) (string, bool) {
	switch typ {
	case "NameProperty":
		return fmt.Sprintf("%q", r.fname()), true
	case "StrProperty":
		return fmt.Sprintf("%q", r.fstr()), true
	case "IntProperty":
		return fmt.Sprintf("%d", r.i32()), true
	case "Int64Property":
		return fmt.Sprintf("%d", r.i64()), true
	case "FloatProperty":
		return fmt.Sprintf("%f", r.f32()), true
	case "ObjectProperty", "SoftObjectProperty":
		// FPackageIndex — 4 bytes. Print raw.
		return fmt.Sprintf("idx=%d", r.i32()), true
	}
	return "", false
}

// dumpAtomicOrStream — tiny shim for "is this an atomic struct or a property
// stream?". Used for both top-level StructProperty bodies and array element
// bodies.
func dumpAtomicOrStream(w io.Writer, r *reader, structType string, depth, arrayMax, sizeBound int) {
	indent := strings.Repeat("  ", depth)
	switch structType {
	case "Guid", "Vector", "Vector2D", "Vector4", "Quat", "Rotator",
		"LinearColor", "Color", "IntPoint", "Box":
		// Reuse the atomic decoder by wrapping in a fake tagInfo.
		dumpStructBody(w, r, tagInfo{structType: structType, size: sizeBound}, depth, arrayMax)
	default:
		fmt.Fprintf(w, "%s(stream)\n", indent)
		dumpPropertyStream(w, r, depth, arrayMax, sizeBound)
	}
}

