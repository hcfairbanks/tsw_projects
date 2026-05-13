// render_groups.go — extract ribbon-grouping info from NetworkRenderComponent
// exports inside TS_*.umap tiles.
//
// The renderer batches ribbons that visually belong to one continuous track
// piece into a NetworkMultiRibbonRange. Two ribbons that appear together in
// such a range are definitely on the same physical track. Two ribbons that
// don't co-occur may still be on the same track (sometimes the renderer
// splits a long track into multiple ranges) — but two ribbons with hard
// evidence that they're in *different* ranges are a different track. The
// path-walker uses this as a chain-or-not constraint to refuse cross-track
// over-chains at parallel-track junctions.
//
// Property path we walk in each NetworkRenderComponent export:
//
//   RenderRangeReference (Struct)
//     RibbonRangeReferences (Array<Struct=NetworkMultiRibbonRange>)
//       Ranges (Array<Struct=NetworkRibbonRange>)
//         RibbonReference (Struct=Guid, 16 bytes inline)
//
// Mirrors the output of c:\temp\tc-recon\extract_ribbon_groups.py so the
// downstream tc-hermite --ribbon-groups flag consumes either source.
package uasset

import (
	"strings"
)

// RibbonGroup is the GUIDs from a single NetworkMultiRibbonRange — the
// renderer's claim that these ribbons are one continuous mesh = one track.
type RibbonGroup struct {
	GUIDs []string // normalised (lowercase 32-char hex)
}

// ParseRibbonGroupsFromUmap opens the .uasset/.uexp pair, walks every
// NetworkRender*Component export (skipping Default__ class templates),
// and returns the ribbon-groupings found inside their RenderRangeReference.
func ParseRibbonGroupsFromUmap(uassetPath string) ([]RibbonGroup, error) {
	u, err := ReadUmap(uassetPath)
	if err != nil {
		return nil, err
	}

	var groups []RibbonGroup
	for _, e := range u.Exports {
		if !isNetworkRenderComponent(e.ObjectName) {
			continue
		}
		pr := u.PropertyReader(e)
		if pr == nil {
			continue
		}
		groups = append(groups, walkNetworkRenderComponent(pr)...)
	}
	return groups, nil
}

// isNetworkRenderComponent matches "NetworkRenderComponent_0",
// "NetworkRenderTurnoutComponent_3", etc., excluding "Default__..." class
// templates which carry no instance data.
func isNetworkRenderComponent(name string) bool {
	if !strings.Contains(name, "NetworkRender") {
		return false
	}
	if !strings.Contains(name, "Component") {
		return false
	}
	if strings.HasPrefix(name, "Default__") {
		return false
	}
	return true
}

// walkNetworkRenderComponent reads the property stream of one export and
// extracts every NetworkMultiRibbonRange's ribbon-GUID set.
func walkNetworkRenderComponent(r *reader) []RibbonGroup {
	var groups []RibbonGroup
	for r.remaining() > 8 {
		t, ok := r.readTag()
		if !ok {
			break
		}
		dp := r.p
		if t.name == "RenderRangeReference" && t.ptype == "StructProperty" {
			groups = append(groups, walkRenderRangeReference(r, dp, t.size)...)
		}
		r.seek(dp + t.size)
	}
	return groups
}

// walkRenderRangeReference parses the body of a RenderRangeReference struct.
// We're looking for a single ArrayProperty named RibbonRangeReferences whose
// elements are NetworkMultiRibbonRange structs.
func walkRenderRangeReference(r *reader, dp, size int) []RibbonGroup {
	end := dp + size
	var groups []RibbonGroup
	for r.p < end {
		t, ok := r.readTag()
		if !ok {
			break
		}
		tdp := r.p
		if t.name == "RibbonRangeReferences" && t.ptype == "ArrayProperty" &&
			t.innerType == "StructProperty" {
			groups = append(groups, walkArrayOfStructs(r, tdp, t.size, walkMultiRibbonRange)...)
		}
		r.seek(tdp + t.size)
	}
	r.seek(end)
	return groups
}

// walkMultiRibbonRange parses one element of a NetworkMultiRibbonRange array
// — the element body is a property stream ending in "None". The only
// property we care about is `Ranges`, an array of NetworkRibbonRange structs.
//
// On exit, `r` is left just past the None tag (so the outer array loop can
// proceed straight into the next element). Do NOT seek to a precomputed end
// here: array elements have no per-element size — the None terminator is
// the only authoritative boundary.
func walkMultiRibbonRange(r *reader, _, sizeBound int) []RibbonGroup {
	end := r.p + sizeBound
	var group RibbonGroup
	for r.p < end {
		t, ok := r.readTag()
		if !ok {
			break // None — element done, r is past the None tag.
		}
		tdp := r.p
		if t.name == "Ranges" && t.ptype == "ArrayProperty" &&
			t.innerType == "StructProperty" {
			// Each Ranges element is a NetworkRibbonRange whose RibbonReference
			// is a Struct=Guid (16 bytes inline). Collect every GUID into one
			// group — that's the "trust set" for this MultiRibbonRange.
			for _, g := range walkArrayOfStructsRet(r, tdp, t.size, walkRibbonRange) {
				if g != "" {
					group.GUIDs = append(group.GUIDs, g)
				}
			}
		}
		r.seek(tdp + t.size)
	}
	if len(group.GUIDs) >= 2 {
		return []RibbonGroup{group}
	}
	return nil
}

// walkRibbonRange parses one NetworkRibbonRange element. Its RibbonReference
// property is a StructProperty whose structType=="Guid" and whose body is
// the 16 raw bytes of an FGuid — formatted via fmtGUID + NormalizeGUID so
// the resulting string matches the canonical form used elsewhere.
//
// Same array-element contract as walkMultiRibbonRange: on exit `r` sits
// just past the None terminator.
func walkRibbonRange(r *reader, _, sizeBound int) string {
	end := r.p + sizeBound
	var guid string
	for r.p < end {
		t, ok := r.readTag()
		if !ok {
			break
		}
		tdp := r.p
		if t.name == "RibbonReference" && t.ptype == "StructProperty" &&
			t.structType == "Guid" {
			var raw [16]byte
			copy(raw[:], r.d[tdp:tdp+16])
			guid = NormalizeGUID(fmtGUID(raw))
		}
		r.seek(tdp + t.size)
	}
	return guid
}

// walkArrayOfStructs is the generic ArrayProperty<StructProperty> walker:
// reads `count` and the inner FPropertyTag, then dispatches each struct body
// (size taken from the inner tag) to a callback that returns the groups it
// found inside.
func walkArrayOfStructs(r *reader, dp, size int, eachElem func(*reader, int, int) []RibbonGroup) []RibbonGroup {
	end := dp + size
	count := int(r.i32())
	if count == 0 {
		r.seek(end)
		return nil
	}
	innerTag, ok := r.readTag()
	if !ok {
		r.seek(end)
		return nil
	}
	// Inner-tag size is the total size of all element bodies combined; each
	// element runs from current position until its own None tag.
	bodyEnd := r.p + innerTag.size
	var out []RibbonGroup
	for i := 0; i < count && r.p < bodyEnd; i++ {
		// Each element's "size" is unknown until we hit its None — pass the
		// remaining body length as a safety bound. The element walker is
		// responsible for advancing `r` by exactly the element it consumed.
		startElem := r.p
		out = append(out, eachElem(r, startElem, bodyEnd-startElem)...)
		// If the callback didn't advance, bail to avoid an infinite loop on
		// malformed data.
		if r.p == startElem {
			break
		}
	}
	r.seek(end)
	return out
}

// walkArrayOfStructsRet is the same as walkArrayOfStructs but returns a flat
// []string per element (rather than []RibbonGroup) — useful when an array's
// elements yield a single value each (like Ranges, where each element gives
// us one ribbon GUID).
func walkArrayOfStructsRet(r *reader, dp, size int, eachElem func(*reader, int, int) string) []string {
	end := dp + size
	count := int(r.i32())
	if count == 0 {
		r.seek(end)
		return nil
	}
	innerTag, ok := r.readTag()
	if !ok {
		r.seek(end)
		return nil
	}
	bodyEnd := r.p + innerTag.size
	out := make([]string, 0, count)
	for i := 0; i < count && r.p < bodyEnd; i++ {
		startElem := r.p
		out = append(out, eachElem(r, startElem, bodyEnd-startElem))
		if r.p == startElem {
			break
		}
	}
	r.seek(end)
	return out
}
