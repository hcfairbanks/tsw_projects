// features_cooked.go — binary-walker extraction of NetworkTurnoutJunction
// (switches), TrackMarkerProperty (platform / junction route markers),
// CarStopSignProperty (cab-stop signs), Signal (trackside signals), and
// LinkedPlatform (station-platform anchors) from a single TT_*.umap.
//
// All data lands as `*uasset.<Type>` records with the same shape the
// existing JSON-based parser produces, so the existing GeoJSON writers in
// internal/output/ can consume them without changes.
package uasset

import (
	"encoding/binary"
	"math"
	"strings"
)

// CookedFeatures holds every feature flavour extracted from one tile.
type CookedFeatures struct {
	Platforms    []*LinkedPlatform
	Signals      []*Signal
	Switches     []*Switch
	CarStopSigns []*CarStopSign
	RouteMarkers []*RouteMarker
	Collectables []*CookedCollectable
}

// CookedCollectable is one in-world collectable spawn point — TSW6's
// "Mastery" items: route maps, scarves, delivery robots, defibrillators,
// posters, and so on. Identified by the presence of a `CollectableID`
// FGuid property on an Actor sub-export. Position is the actor's
// RootComponent.RelativeLocation lifted into world cm via the tile origin.
type CookedCollectable struct {
	TileName       string  `json:"tile,omitempty"`
	ActorClass     string  `json:"actor_class"` // e.g. "BP_Robot_EMK"
	InstanceName   string  `json:"instance"`    // e.g. "BP_Robot_EMK_01_2"
	CollectableID  string  `json:"collectable_id"`
	X, Y, Z        float64 `json:"-"` // tile-local cm
}

// ParseCookedFeaturesFromUmap walks one .umap and returns every supported
// feature instance. Tile name is used to populate the .TileName field of
// each record (caller passes it in since the package doesn't know paths).
func ParseCookedFeaturesFromUmap(uassetPath, tileName string) (*CookedFeatures, error) {
	u, err := ReadUmap(uassetPath)
	if err != nil {
		return nil, err
	}
	out := &CookedFeatures{}

	// Build ribbon-export-index → ribbon-GUID map (1-based FPackageIndex).
	// Used for sub-export linkage (CarStopSign / TrackMarker via OuterIndex)
	// and for signal-via-NetworkProperties resolution.
	ribbonGUIDByIdx := map[int32]string{}
	for i, e := range u.Exports {
		if !isNetworkRibbonExport(e.ObjectName) {
			continue
		}
		pr := u.PropertyReader(e)
		if pr == nil {
			continue
		}
		guid, _ := walkRibbonForGuidAndCurve(pr)
		if guid != "" {
			ribbonGUIDByIdx[int32(i+1)] = guid
		}
	}

	for i, e := range u.Exports {
		switch {
		case strings.HasPrefix(e.ObjectName, "NetworkTurnoutJunction") &&
			!strings.HasPrefix(e.ObjectName, "Default__"):
			pr := u.PropertyReader(e)
			if pr == nil {
				continue
			}
			s := &Switch{TileName: tileName}
			walkNetworkTurnoutJunction(pr, s)
			if s.JctGUID != "" || s.NodeGUID != "" {
				out.Switches = append(out.Switches, s)
			}
		case strings.HasPrefix(e.ObjectName, "CarStopSignProperty") ||
			strings.HasPrefix(e.ObjectName, "Default__CarStopSignProperty"):
			// "Default__" variants in TSW6 carry real data — they're not
			// blueprint CDOs in this context. Verified against existing
			// JSON-based parser.
			parentGUID := ribbonGUIDByIdx[e.OuterIndex]
			if parentGUID == "" {
				continue
			}
			pr := u.PropertyReader(e)
			if pr == nil {
				continue
			}
			c := &CarStopSign{RibbonGUID: parentGUID, TileName: tileName}
			walkCarStopSignProperty(pr, c)
			out.CarStopSigns = append(out.CarStopSigns, c)
		case strings.HasPrefix(e.ObjectName, "TrackMarkerProperty") ||
			strings.HasPrefix(e.ObjectName, "Default__TrackMarkerProperty"):
			parentGUID := ribbonGUIDByIdx[e.OuterIndex]
			if parentGUID == "" {
				continue
			}
			pr := u.PropertyReader(e)
			if pr == nil {
				continue
			}
			m := &RouteMarker{RibbonGUID: parentGUID, TileName: tileName}
			walkTrackMarkerProperty(pr, m)
			if m.Name != "" {
				out.RouteMarkers = append(out.RouteMarkers, m)
			}
		}
		_ = i
	}

	// Platforms come from a `LinkedPlatforms` Array<Struct> property — most
	// commonly on a "RouteDefinition" or "TrackNetworkActor" export. Try
	// walking every export looking for that property name.
	for _, e := range u.Exports {
		pr := u.PropertyReader(e)
		if pr == nil {
			continue
		}
		walkForLinkedPlatforms(pr, tileName, &out.Platforms)
	}

	// Signals: two-step. First pre-scan every export for the signal-shaped
	// trio (SignalID + SignalType + Location), caching by 1-based export
	// index. Then walk each NetworkRibbon's NetworkProperties /
	// NetworkPropertiesReverseOrder arrays for refs into that cache.
	type sigInfo struct {
		id, sigType string
		location    float32
	}
	signalCache := map[int32]sigInfo{}
	for i, e := range u.Exports {
		if strings.HasPrefix(e.ObjectName, "Default__") {
			continue
		}
		pr := u.PropertyReader(e)
		if pr == nil {
			continue
		}
		s, ok := walkForSignalFields(pr)
		if !ok {
			continue
		}
		signalCache[int32(i+1)] = s
	}
	if len(signalCache) > 0 {
		seen := map[string]bool{}
		for i, e := range u.Exports {
			if !isNetworkRibbonExport(e.ObjectName) {
				continue
			}
			ribbonGUID := ribbonGUIDByIdx[int32(i+1)]
			if ribbonGUID == "" {
				continue
			}
			pr := u.PropertyReader(e)
			if pr == nil {
				continue
			}
			refs := walkRibbonNetworkPropertyRefs(pr)
			for _, ref := range refs {
				info, ok := signalCache[ref]
				if !ok {
					continue
				}
				if seen[info.id] {
					continue
				}
				seen[info.id] = true
				out.Signals = append(out.Signals, &Signal{
					SignalID:         info.id,
					SignalType:       info.sigType,
					RibbonGUID:       ribbonGUID,
					LocationFraction: info.location,
					TileName:         tileName,
				})
			}
		}
	}

	return out, nil
}

// walkForSignalFields returns the (SignalID, SignalType, Location) trio if
// an export looks like a signal — i.e. has at least SignalID and Location.
// Type may be empty if the enum field is missing.
func walkForSignalFields(r *reader) (s struct {
	id, sigType string
	location    float32
}, ok bool) {
	defer func() { _ = recover() }()
	hasID, hasLoc := false, false
	for r.remaining() > 8 {
		t, more := r.readTag()
		if !more {
			break
		}
		dp := r.p
		switch {
		case t.name == "SignalID" && t.ptype == "StrProperty":
			s.id = r.fstr()
			hasID = s.id != ""
		case t.name == "SignalType" && (t.ptype == "ByteProperty" || t.ptype == "EnumProperty"):
			s.sigType = lastSegmentAfter(r.fname(), "::")
		case t.name == "SignalType" && t.ptype == "NameProperty":
			s.sigType = lastSegmentAfter(r.fname(), "::")
		case t.name == "Location" && t.ptype == "FloatProperty":
			s.location = r.f32()
			hasLoc = true
		}
		r.seek(dp + t.size)
	}
	if hasID && hasLoc {
		ok = true
	}
	return
}


// walkRibbonNetworkPropertyRefs returns every export-index referenced by a
// NetworkRibbon's NetworkProperties + NetworkPropertiesReverseOrder
// ArrayProperty<Object> values. Indexes are 1-based FPackageIndex (positive
// for exports). Negative ones (imports) are filtered out.
func walkRibbonNetworkPropertyRefs(r *reader) []int32 {
	defer func() { _ = recover() }()
	var refs []int32
	for r.remaining() > 8 {
		t, more := r.readTag()
		if !more {
			break
		}
		dp := r.p
		if (t.name == "NetworkProperties" || t.name == "NetworkPropertiesReverseOrder") &&
			t.ptype == "ArrayProperty" {
			ar := &reader{d: r.d[dp : dp+t.size], nm: r.nm}
			refs = append(refs, readObjectArrayInts(ar)...)
		}
		r.seek(dp + t.size)
	}
	return refs
}

// readObjectArrayInts reads the body of an `ArrayProperty<ObjectProperty>`.
// In TSW6 cooked binary the layout is simply:
//   int32 count
//   <count> int32 FPackageIndex values
// (No inner tag — struct-typed arrays do have one, but object-typed arrays
// just have the raw int32 element bodies. Empirically size matches
// 4 + N*4 for ObjectProperty arrays.) Returns positive (export) indices
// only; imports (negative) are dropped.
func readObjectArrayInts(r *reader) []int32 {
	defer func() { _ = recover() }()
	if r.remaining() < 4 {
		return nil
	}
	count := int(r.i32())
	if count <= 0 || count > 100000 {
		return nil
	}
	out := make([]int32, 0, count)
	for i := 0; i < count && r.remaining() >= 4; i++ {
		v := r.i32()
		if v > 0 {
			out = append(out, v)
		}
	}
	return out
}

// walkNetworkTurnoutJunction extracts JctGuid, NodeGuid, the three Connection
// ribbon refs (Ingoing / Outgoing / Turnout), and bManuallyControlled from a
// switch's property stream.
func walkNetworkTurnoutJunction(r *reader, s *Switch) {
	for r.remaining() > 8 {
		t, ok := r.readTag()
		if !ok {
			return
		}
		dp := r.p
		switch {
		case t.name == "JctGuid" && t.ptype == "StructProperty" && t.structType == "Guid":
			var raw [16]byte
			copy(raw[:], r.d[dp:dp+16])
			s.JctGUID = NormalizeGUID(fmtGUID(raw))
		case t.name == "NodeGuid" && t.ptype == "StructProperty" && t.structType == "Guid":
			var raw [16]byte
			copy(raw[:], r.d[dp:dp+16])
			s.NodeGUID = NormalizeGUID(fmtGUID(raw))
		case t.name == "IngoingRibbonConnection" && t.ptype == "StructProperty":
			s.IngoingRibbon = readConnectionRibbonGUID(&reader{d: r.d[dp : dp+t.size], nm: r.nm})
		case t.name == "OutgoingRibbonConnection" && t.ptype == "StructProperty":
			s.OutgoingRibbon = readConnectionRibbonGUID(&reader{d: r.d[dp : dp+t.size], nm: r.nm})
		case t.name == "TurnoutRibbonConnection" && t.ptype == "StructProperty":
			s.TurnoutRibbon = readConnectionRibbonGUID(&reader{d: r.d[dp : dp+t.size], nm: r.nm})
		case t.name == "bManuallyControlled" && t.ptype == "BoolProperty":
			s.ManuallyControlled = t.boolVal != 0
		}
		r.seek(dp + t.size)
	}
}

// readConnectionRibbonGUID walks a Connection sub-struct and pulls the inner
// RibbonGuid (FGuid). Defensive: if the body is malformed, returns "".
func readConnectionRibbonGUID(r *reader) (out string) {
	defer func() {
		if rec := recover(); rec != nil {
			out = ""
		}
	}()
	for r.remaining() >= 24 {
		t, ok := r.readTag()
		if !ok {
			return
		}
		dp := r.p
		if t.name == "RibbonGuid" && t.ptype == "StructProperty" && t.structType == "Guid" {
			var raw [16]byte
			copy(raw[:], r.d[dp:dp+16])
			out = NormalizeGUID(fmtGUID(raw))
			return
		}
		r.seek(dp + t.size)
	}
	return ""
}

// walkCarStopSignProperty pulls Location (0..1 along ribbon) and
// MaxNumberOfRailVehicles from a CarStopSignProperty sub-export.
func walkCarStopSignProperty(r *reader, c *CarStopSign) {
	for r.remaining() > 8 {
		t, ok := r.readTag()
		if !ok {
			return
		}
		dp := r.p
		switch {
		case t.name == "Location" && t.ptype == "FloatProperty":
			c.Location = r.f32()
		case t.name == "MaxNumberOfRailVehicles" && t.ptype == "IntProperty":
			c.MaxNumberOfRailVehicles = int(r.i32())
		}
		r.seek(dp + t.size)
	}
}

// walkTrackMarkerProperty pulls MarkerName / DisplayName, MarkerType,
// LineSide, Location, Start, End from a TrackMarkerProperty sub-export.
//
// Empirical TC layout: `MarkerName : NameProperty = "Metro Loop Track 3"`
// and `DisplayName : Text = "Metro Loop Track 3"`. The two usually agree
// but DisplayName has nicer formatting in some routes.
func walkTrackMarkerProperty(r *reader, m *RouteMarker) {
	var displayName, markerName string
	for r.remaining() > 8 {
		t, ok := r.readTag()
		if !ok {
			break
		}
		dp := r.p
		switch t.ptype {
		case "FloatProperty":
			switch t.name {
			case "Location":
				m.Location = r.f32()
			case "Start":
				m.Start = r.f32()
			case "End":
				m.End = r.f32()
			}
		case "StrProperty":
			if t.name == "MarkerName" {
				markerName = r.fstr()
			}
		case "TextProperty":
			if t.name == "DisplayName" {
				displayName = r.ftext(t.size)
			} else if t.name == "MarkerName" {
				if v := r.ftext(t.size); v != "" {
					markerName = v
				}
			}
		case "NameProperty":
			switch t.name {
			case "MarkerName":
				markerName = r.fname()
			case "MarkerType":
				m.MarkerType = lastSegmentAfter(r.fname(), "::")
			case "LineSide":
				m.LineSide = lastSegmentAfter(r.fname(), "::")
			}
		case "ByteProperty", "EnumProperty":
			switch t.name {
			case "MarkerType":
				m.MarkerType = lastSegmentAfter(r.fname(), "::")
			case "LineSide":
				m.LineSide = lastSegmentAfter(r.fname(), "::")
			}
		}
		r.seek(dp + t.size)
	}
	// Prefer DisplayName when available; otherwise the FName-form MarkerName.
	if displayName != "" {
		m.Name = displayName
	} else {
		m.Name = markerName
	}
}

// walkForLinkedPlatforms scans an export's properties for a LinkedPlatforms
// array. Each entry is a RouteLocation { Name; Location { RibbonReference; RibbonLocation } }.
func walkForLinkedPlatforms(r *reader, tileName string, out *[]*LinkedPlatform) {
	defer func() { _ = recover() }()
	for r.remaining() > 8 {
		t, ok := r.readTag()
		if !ok {
			return
		}
		dp := r.p
		if t.name == "LinkedPlatforms" && t.ptype == "ArrayProperty" {
			parseLinkedPlatformsArray(&reader{d: r.d[dp : dp+t.size], nm: r.nm}, tileName, out)
		}
		r.seek(dp + t.size)
	}
}

// parseLinkedPlatformsArray reads the body of an ArrayProperty whose elements
// are RouteLocation structs. ArrayProperty body layout:
//   inner-tag (one FPropertyTag describing the element type/struct)
//   element_count (int32)
//   then `count` raw struct bodies back-to-back.
func parseLinkedPlatformsArray(r *reader, tileName string, out *[]*LinkedPlatform) {
	innerTag, ok := r.readTag()
	if !ok || innerTag.size <= 0 {
		return
	}
	if r.remaining() < 4 {
		return
	}
	count := int(r.i32())
	if count < 0 || count > 10000 {
		return
	}
	// Each element body is `innerTag.size` bytes? No — innerTag.size for the
	// inner header is 0 in array contexts. Element layout is just struct
	// bodies (same shape as a top-level StructProperty body would be).
	// We walk each entry's tagged inner stream until "None".
	for i := 0; i < count && r.remaining() > 8; i++ {
		lp := &LinkedPlatform{TileName: tileName}
		readRouteLocationEntry(r, lp)
		if lp.Name != "" && lp.RibbonGUID != "" {
			*out = append(*out, lp)
		}
	}
}

// readRouteLocationEntry walks one RouteLocation struct entry's tagged
// property stream, looking for Name (FString) and Location (nested
// NetworkRibbonLocation with RibbonReference + RibbonLocation).
func readRouteLocationEntry(r *reader, lp *LinkedPlatform) {
	for r.remaining() > 8 {
		t, ok := r.readTag()
		if !ok {
			return
		}
		dp := r.p
		switch {
		case t.name == "Name" && t.ptype == "StrProperty":
			lp.Name = r.fstr()
		case t.name == "Location" && t.ptype == "StructProperty":
			readRibbonLocation(&reader{d: r.d[dp : dp+t.size], nm: r.nm}, lp)
		}
		r.seek(dp + t.size)
	}
}

// readRibbonLocation walks a NetworkRibbonLocation sub-struct, pulling
// RibbonReference (FGuid) and RibbonLocation (Float).
func readRibbonLocation(r *reader, lp *LinkedPlatform) {
	defer func() { _ = recover() }()
	for r.remaining() > 8 {
		t, ok := r.readTag()
		if !ok {
			return
		}
		dp := r.p
		switch {
		case t.name == "RibbonReference" && t.ptype == "StructProperty" && t.structType == "Guid":
			var raw [16]byte
			copy(raw[:], r.d[dp:dp+16])
			lp.RibbonGUID = NormalizeGUID(fmtGUID(raw))
		case t.name == "RibbonLocation" && t.ptype == "FloatProperty":
			lp.RibbonLocation = r.f32()
		}
		r.seek(dp + t.size)
	}
}

// ParseCookedCollectablesFromUmap walks one .umap (typically ST_*.umap)
// looking for actor exports that carry a `CollectableID` Guid. Each match
// is emitted as a CookedCollectable record with its tile-local position
// pulled from the actor's RootComponent.RelativeLocation. Caller adds the
// tile world offset before projecting to lat/lng.
func ParseCookedCollectablesFromUmap(uassetPath, tileName string) ([]*CookedCollectable, error) {
	u, err := ReadUmap(uassetPath)
	if err != nil {
		return nil, err
	}
	// Pass 1: scan every export, look for a `CollectableID` property +
	// note its RootComponent ref (FPackageIndex into another export).
	type cand struct {
		idx       int32 // 1-based FPackageIndex of the actor export
		guid      string
		rootIdx   int32 // 1-based ref to the actor's RootComponent
		actorName string
	}
	var cands []cand
	for i, e := range u.Exports {
		if strings.HasPrefix(e.ObjectName, "Default__") {
			continue
		}
		pr := u.PropertyReader(e)
		if pr == nil {
			continue
		}
		guid, rootIdx, ok := walkActorForCollectable(pr)
		if !ok {
			continue
		}
		cands = append(cands, cand{
			idx:       int32(i + 1),
			guid:      guid,
			rootIdx:   rootIdx,
			actorName: e.ObjectName,
		})
	}
	if len(cands) == 0 {
		return nil, nil
	}
	// Pass 2: follow each actor's RootComponent to find RelativeLocation.
	out := make([]*CookedCollectable, 0, len(cands))
	for _, c := range cands {
		var x, y, z float64
		var hasPos bool
		if c.rootIdx > 0 && int(c.rootIdx) <= len(u.Exports) {
			rootExp := u.Exports[c.rootIdx-1]
			rpr := u.PropertyReader(rootExp)
			if rpr != nil {
				x, y, z, hasPos = walkSceneCompForLocation(rpr)
			}
		}
		if !hasPos {
			// Fallback: some collectables put RelativeLocation directly
			// on the actor body (no separate root sub-export).
			if pr := u.PropertyReader(u.Exports[c.idx-1]); pr != nil {
				x, y, z, hasPos = walkSceneCompForLocation(pr)
			}
		}
		if !hasPos {
			continue
		}
		out = append(out, &CookedCollectable{
			TileName:      tileName,
			ActorClass:    actorClassFromName(c.actorName),
			InstanceName:  c.actorName,
			CollectableID: c.guid,
			X:             x,
			Y:             y,
			Z:             z,
		})
	}
	return out, nil
}

// walkActorForCollectable returns (CollectableID guid, RootComponent FPackageIndex, true)
// when the export has BOTH fields. Order-agnostic.
func walkActorForCollectable(r *reader) (guid string, rootIdx int32, ok bool) {
	defer func() { _ = recover() }()
	hasGUID, hasRoot := false, false
	for r.remaining() > 8 {
		t, more := r.readTag()
		if !more {
			break
		}
		dp := r.p
		switch {
		case t.name == "CollectableID" && t.ptype == "StructProperty" && t.structType == "Guid":
			var raw [16]byte
			copy(raw[:], r.d[dp:dp+16])
			guid = NormalizeGUID(fmtGUID(raw))
			hasGUID = guid != ""
		case t.name == "RootComponent" && t.ptype == "ObjectProperty":
			rootIdx = r.i32()
			hasRoot = rootIdx > 0
		}
		r.seek(dp + t.size)
	}
	ok = hasGUID && hasRoot
	return
}

// walkSceneCompForLocation reads a SceneComponent-style export and pulls
// RelativeLocation as world cm (FVector — 3 floats). Returns ok=false if
// the property is missing.
func walkSceneCompForLocation(r *reader) (x, y, z float64, ok bool) {
	defer func() { _ = recover() }()
	for r.remaining() > 8 {
		t, more := r.readTag()
		if !more {
			break
		}
		dp := r.p
		if t.name == "RelativeLocation" && t.ptype == "StructProperty" {
			// FVector: 3 float32. Different size could indicate
			// FVector3d (3 doubles, 24 bytes); handle both.
			switch {
			case t.size >= 24:
				x = readF64(r.d[dp:])
				y = readF64(r.d[dp+8:])
				z = readF64(r.d[dp+16:])
			case t.size >= 12:
				x = readF32(r.d[dp:])
				y = readF32(r.d[dp+4:])
				z = readF32(r.d[dp+8:])
			}
			ok = true
			// Don't break — keep reading; multiple components might exist
			// but we want the first RelativeLocation we see (the actor's).
		}
		r.seek(dp + t.size)
	}
	return
}

func readF32(b []byte) float64 {
	if len(b) < 4 {
		return 0
	}
	return float64(math.Float32frombits(binary.LittleEndian.Uint32(b)))
}

// actorClassFromName strips trailing _N suffixes from an instance name to
// recover the underlying BP class (e.g. "BP_Robot_EMK_01_2" → "BP_Robot_EMK_01").
// We strip ONE trailing "_<digits>" — the BP class itself often ends in
// "_01", which we want to preserve.
func actorClassFromName(name string) string {
	i := strings.LastIndexByte(name, '_')
	if i < 0 || i+1 >= len(name) {
		return name
	}
	tail := name[i+1:]
	for _, c := range tail {
		if c < '0' || c > '9' {
			return name
		}
	}
	return name[:i]
}

// lastSegmentAfter returns the part of s after the last occurrence of sep,
// or s itself if sep isn't present.
func lastSegmentAfter(s, sep string) string {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s
	}
	return s[i+len(sep):]
}
