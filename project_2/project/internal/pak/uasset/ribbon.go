package uasset

import (
	"encoding/json"
	"os"
)

// Ribbon describes one NetworkRibbon record from a tile's .umap.json.
// Fields are in UE centimetres where applicable (the same units stored in
// the asset). Conversion to lat/lng is done downstream using `internal/geo`.
type Ribbon struct {
	GUID          string  `json:"guid"`           // RibbonGuid (hex string)
	StartNodeGUID string  `json:"start_node_guid,omitempty"`
	EndNodeGUID   string  `json:"end_node_guid,omitempty"`
	TileName      string  `json:"tile,omitempty"` // e.g. "LT_x-10_y0" — from the source umap filename
	// Anchor (legacy): WorldLocation.X/Y (UE cm). The original parser looked
	// for a WorldLocation property on NetworkRibbon, but that property does
	// not actually exist on TSW6's NetworkRibbon export — these stayed zero.
	// Retained for backward compatibility with existing on-disk indices.
	WorldX     float64 `json:"world_x,omitempty"`
	WorldY     float64 `json:"world_y,omitempty"`
	HasAnchor  bool    `json:"has_anchor,omitempty"`
	// CachedStart{X,Y,Z}: the actual world-space position of the ribbon's
	// t=0 point, taken from the NetworkRibbon's `CachedStartPosition`
	// (FarVector). Combined with ArcDelta() over the curve geometry below,
	// this gives a frame-invariant world position at any arc length —
	// works whether the curve is stored in absolute or local coordinates.
	CachedStartX   float64 `json:"cached_start_x,omitempty"`
	CachedStartY   float64 `json:"cached_start_y,omitempty"`
	CachedStartZ   float64 `json:"cached_start_z,omitempty"`
	HasCachedStart bool    `json:"has_cached_start,omitempty"`
	// Curve geometry (from the Curve export the NetworkRibbon references).
	// StartX/StartY may be in either world or local coordinates depending on
	// the ribbon — only TangentX/Y, Radius, Length are needed to reproduce
	// the arc shape; the world anchor comes from CachedStart{X,Y}.
	StartX     float64 `json:"arc_start_x,omitempty"`
	StartY     float64 `json:"arc_start_y,omitempty"`
	TangentX   float64 `json:"arc_tangent_x,omitempty"`
	TangentY   float64 `json:"arc_tangent_y,omitempty"`
	Radius     float64 `json:"arc_radius,omitempty"`
	Length     float64 `json:"arc_length,omitempty"`
}

// LinkedPlatform is one named (RouteLocation) entry attached to a station
// blueprint actor in a tile. These are the source-of-truth list of physical
// platforms for that station — they exist whether or not any service stops
// at them.
type LinkedPlatform struct {
	Name           string  `json:"name"`
	RibbonGUID     string  `json:"ribbon_guid"`
	RibbonLocation float32 `json:"ribbon_location"`
	TileName       string  `json:"tile,omitempty"`
}

// Signal describes one signal actor positioned on the rail network.
// SignalID is the route-specific identifier (e.g. "WFP_G2"); SignalType is
// the semantic class (Shunt / Repeater / Distant / Home / etc, from
// ESignalType). LocationFraction is 0..1 along the parent ribbon.
type Signal struct {
	SignalID         string  `json:"signal_id"`
	SignalType       string  `json:"signal_type,omitempty"`
	RibbonGUID       string  `json:"ribbon_guid"`
	LocationFraction float32 `json:"location_fraction"`
	TileName         string  `json:"tile,omitempty"`
}

// Switch describes a NetworkTurnoutJunction — a track switch / point.
// The switch position is at the start of the OutgoingRibbon (which equals
// the end of the IngoingRibbon and the start of the TurnoutRibbon).
// JctGUID + NodeGUID identify the junction inside the rail graph.
type Switch struct {
	JctGUID          string `json:"jct_guid"`
	NodeGUID         string `json:"node_guid,omitempty"`
	IngoingRibbon    string `json:"ingoing_ribbon,omitempty"`
	OutgoingRibbon   string `json:"outgoing_ribbon,omitempty"`
	TurnoutRibbon    string `json:"turnout_ribbon,omitempty"`
	ManuallyControlled bool `json:"manually_controlled,omitempty"`
	TileName         string `json:"tile,omitempty"`
}

// CarStopSign is the in-game "Car Stop" sign — the green ring the driver sees
// at a platform marking where the cab must stop. Each platform direction has
// its own sign per car-class. MaxNumberOfRailVehicles=0 means "default/any".
// The sign's position is its parent ribbon's geometry walked to scalar Location.
// Found as a sub-export of NetworkRibbon (UE4 OuterIndex parent relationship)
// — NOT in the ribbon's NetworkProperties array.
type CarStopSign struct {
	RibbonGUID             string  `json:"ribbon_guid"`
	Location               float32 `json:"location"`                       // 0..1 along ribbon
	MaxNumberOfRailVehicles int    `json:"max_rail_vehicles"`              // 0 = default/any
	TileName               string  `json:"tile,omitempty"`
}

// RouteMarker is a TrackMarkerProperty attached to a NetworkRibbon — covers
// platform markers (MarkerType=Platform, MarkerName like "Lake Platform 1"),
// junction routing markers (MarkerName like "Smallbrook Junction Line 1"),
// and other named track features. End/Start are 0..1 scalars along the parent
// ribbon; Location is the same when only a single point is defined.
type RouteMarker struct {
	RibbonGUID string  `json:"ribbon_guid"`
	Name       string  `json:"name"`
	MarkerType string  `json:"marker_type,omitempty"` // "Platform", "Junction", "" etc.
	LineSide   string  `json:"line_side,omitempty"`
	Location   float32 `json:"location,omitempty"`
	Start      float32 `json:"start,omitempty"`
	End        float32 `json:"end,omitempty"`
	TileName   string  `json:"tile,omitempty"`
}

// ParseRibbonsFromUmap is a thin wrapper around ParseTileFeaturesFromUmap
// for callers that only need ribbons (e.g. the global ribbon-index builder).
func ParseRibbonsFromUmap(jsonPath, tileName string) ([]*Ribbon, error) {
	rs, _, _, _, _, _, err := ParseTileFeaturesFromUmap(jsonPath, tileName)
	return rs, err
}

// ParseTileFeaturesFromUmap reads a converted tile .umap.json and extracts
// every NetworkRibbon export, every LinkedPlatforms RouteLocation entry,
// every Signal export referenced by a ribbon's NetworkProperties array,
// every NetworkTurnoutJunction (switch), every CarStopSignProperty sub-export,
// and every TrackMarkerProperty sub-export. Doing all in one pass amortises
// the JSON load so per-route extracts don't pay the umap conversion cost
// multiple times.
func ParseTileFeaturesFromUmap(jsonPath, tileName string) ([]*Ribbon, []*LinkedPlatform, []*Signal, []*Switch, []*CarStopSign, []*RouteMarker, error) {
	rs, err := parseRibbonsAndExtras(jsonPath, tileName, true)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	return rs.ribbons, rs.platforms, rs.signals, rs.switches, rs.carStopSigns, rs.routeMarkers, nil
}

type tileScanResult struct {
	ribbons      []*Ribbon
	platforms    []*LinkedPlatform
	signals      []*Signal
	switches     []*Switch
	carStopSigns []*CarStopSign
	routeMarkers []*RouteMarker
}

// tileExport mirrors the subset of UAssetGUI's Export schema we care about.
// OuterIndex is the 1-indexed parent export reference (0 = no parent), used
// to find sub-exports of NetworkRibbon (CarStopSignProperty, TrackMarkerProperty).
type tileExport struct {
	ObjectName string            `json:"ObjectName"`
	OuterIndex int               `json:"OuterIndex"`
	Data       []json.RawMessage `json:"Data"`
}

func parseRibbonsAndExtras(jsonPath, tileName string, withPlatforms bool) (tileScanResult, error) {
	rs, err := parseTile(jsonPath, tileName, withPlatforms)
	return rs, err
}

func parseTile(jsonPath, tileName string, withPlatforms bool) (tileScanResult, error) {
	var result tileScanResult
	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		return result, err
	}
	var doc struct {
		Exports []tileExport `json:"Exports"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return result, err
	}
	type prop struct {
		Name  string          `json:"Name"`
		Type  string          `json:"$type"`
		Value json.RawMessage `json:"Value"`
	}
	// Helper: decode a "list of NamePropertyData-like" and grab the first .Value
	firstListValue := func(v json.RawMessage) (string, bool) {
		var arr []struct {
			Value string `json:"Value"`
		}
		if err := json.Unmarshal(v, &arr); err == nil && len(arr) > 0 {
			return arr[0].Value, true
		}
		return "", false
	}
	// Helper: decode a "list containing Vector/Vector2D struct at [0]"
	firstListXY := func(v json.RawMessage) (x, y float64, ok bool) {
		var arr []struct {
			Value struct {
				X float64 `json:"X"`
				Y float64 `json:"Y"`
			} `json:"Value"`
		}
		if err := json.Unmarshal(v, &arr); err == nil && len(arr) > 0 {
			return arr[0].Value.X, arr[0].Value.Y, true
		}
		return 0, 0, false
	}
	// firstListXYZ decodes a struct property whose Value is a list of named
	// scalars { Name: "X"|"Y"|"Z", Value: float } — the shape UAssetGUI
	// emits for FarVector / FVector. Used for CachedStartPosition which is
	// laid out flat (not wrapped in an inner Value struct like FVector2D).
	firstListXYZ := func(v json.RawMessage) (x, y, z float64, ok bool) {
		// Shape A: flat list of named DoubleProperty { Name, Value }
		var flat []struct {
			Name  string  `json:"Name"`
			Value float64 `json:"Value"`
		}
		if err := json.Unmarshal(v, &flat); err == nil && len(flat) > 0 {
			seen := false
			for _, e := range flat {
				switch e.Name {
				case "X":
					x = e.Value
					seen = true
				case "Y":
					y = e.Value
					seen = true
				case "Z":
					z = e.Value
					seen = true
				}
			}
			if seen {
				return x, y, z, true
			}
		}
		// Shape B: list with inner Value struct (same shape as firstListXY)
		var nested []struct {
			Value struct {
				X float64 `json:"X"`
				Y float64 `json:"Y"`
				Z float64 `json:"Z"`
			} `json:"Value"`
		}
		if err := json.Unmarshal(v, &nested); err == nil && len(nested) > 0 {
			return nested[0].Value.X, nested[0].Value.Y, nested[0].Value.Z, true
		}
		return 0, 0, 0, false
	}
	out := make([]*Ribbon, 0)
	for _, e := range doc.Exports {
		if e.ObjectName == "" || len(e.Data) == 0 {
			continue
		}
		// NetworkRibbon export
		if !containsNetworkRibbon(e.ObjectName) {
			continue
		}
		r := &Ribbon{TileName: tileName}
		var curveRef int
		for _, rawProp := range e.Data {
			var p prop
			if err := json.Unmarshal(rawProp, &p); err != nil {
				continue
			}
			switch p.Name {
			case "RibbonGuid":
				if v, ok := firstListValue(p.Value); ok {
					r.GUID = v
				}
			case "StartNodeGuid":
				if v, ok := firstListValue(p.Value); ok {
					r.StartNodeGUID = v
				}
			case "EndNodeGuid":
				if v, ok := firstListValue(p.Value); ok {
					r.EndNodeGUID = v
				}
			case "Curve":
				var n int
				if err := json.Unmarshal(p.Value, &n); err == nil {
					curveRef = n
				}
			case "WorldLocation":
				if x, y, ok := firstListXY(p.Value); ok {
					r.WorldX = x
					r.WorldY = y
					r.HasAnchor = true
				}
			case "CachedStartPosition":
				if x, y, z, ok := firstListXYZ(p.Value); ok {
					r.CachedStartX = x
					r.CachedStartY = y
					r.CachedStartZ = z
					r.HasCachedStart = true
				}
			}
		}
		if curveRef > 0 {
			ci := curveRef - 1
			if ci >= 0 && ci < len(doc.Exports) {
				for _, rawProp := range doc.Exports[ci].Data {
					var p prop
					if err := json.Unmarshal(rawProp, &p); err != nil {
						continue
					}
					switch p.Name {
					case "StartPosition2D":
						if x, y, ok := firstListXY(p.Value); ok {
							r.StartX = x
							r.StartY = y
						}
					case "StartTangent2D":
						if x, y, ok := firstListXY(p.Value); ok {
							r.TangentX = x
							r.TangentY = y
						}
					case "Radius":
						var f float64
						if err := json.Unmarshal(p.Value, &f); err == nil {
							r.Radius = f
						}
					case "Length":
						var f float64
						if err := json.Unmarshal(p.Value, &f); err == nil {
							r.Length = f
						}
					}
				}
			}
		}
		if r.GUID != "" && r.Length > 0 {
			out = append(out, r)
		}
	}
	result.ribbons = out

	if withPlatforms {
		result.platforms = parseLinkedPlatformsInDoc(doc.Exports, tileName, firstListValue)
		// Map ribbon export-index → ribbon GUID so we can look up which ribbon
		// references each signal export.
		ribbonGUIDByExportIndex := map[int]string{}
		for i, e := range doc.Exports {
			if !containsNetworkRibbon(e.ObjectName) {
				continue
			}
			for _, rawProp := range e.Data {
				var p prop
				if err := json.Unmarshal(rawProp, &p); err != nil {
					continue
				}
				if p.Name == "RibbonGuid" {
					if v, ok := firstListValue(p.Value); ok {
						ribbonGUIDByExportIndex[i+1] = v
					}
					break
				}
			}
		}
		result.signals = parseSignalsInDoc(doc.Exports, tileName, ribbonGUIDByExportIndex)
		result.switches = parseSwitchesInDoc(doc.Exports, tileName, firstListValue)
		result.carStopSigns = parseCarStopSignsInDoc(doc.Exports, tileName, ribbonGUIDByExportIndex)
		result.routeMarkers = parseRouteMarkersInDoc(doc.Exports, tileName, ribbonGUIDByExportIndex)
	}
	return result, nil
}

// parseCarStopSignsInDoc finds every CarStopSignProperty export and links it
// to its parent NetworkRibbon via OuterIndex (UE4 sub-export relationship).
// CarStopSigns are NOT in the ribbon's NetworkProperties array; they live as
// child exports. Each captures Location (0..1 along ribbon) and
// MaxNumberOfRailVehicles (0=any, 2=2-car formation, etc.).
func parseCarStopSignsInDoc(exports []tileExport, tileName string, ribbonGUIDByExportIndex map[int]string) []*CarStopSign {
	type prop struct {
		Name  string          `json:"Name"`
		Value json.RawMessage `json:"Value"`
	}
	out := []*CarStopSign{}
	for _, e := range exports {
		// Match CarStopSignProperty / CarStopSignProperty_N / Default__CarStopSignProperty.
		// Default__ is NOT a CDO template here — TSW6 uses it as the export name
		// for the single instance per parent ribbon, and it carries real data.
		if !startsWith(e.ObjectName, "CarStopSignProperty") &&
			!startsWith(e.ObjectName, "Default__CarStopSignProperty") {
			continue
		}
		ribbonGUID := ribbonGUIDByExportIndex[e.OuterIndex]
		if ribbonGUID == "" {
			continue
		}
		c := &CarStopSign{RibbonGUID: ribbonGUID, TileName: tileName}
		for _, rawProp := range e.Data {
			var p prop
			if err := json.Unmarshal(rawProp, &p); err != nil {
				continue
			}
			switch p.Name {
			case "Location":
				var f float32
				if err := json.Unmarshal(p.Value, &f); err == nil {
					c.Location = f
				}
			case "MaxNumberOfRailVehicles":
				var n int
				if err := json.Unmarshal(p.Value, &n); err == nil {
					c.MaxNumberOfRailVehicles = n
				}
			}
		}
		out = append(out, c)
	}
	return out
}

// parseRouteMarkersInDoc finds every TrackMarkerProperty export, links it to
// its parent NetworkRibbon via OuterIndex, and captures MarkerName, MarkerType
// (Platform / Junction / etc.), LineSide, and the scalar position on the
// parent ribbon. Covers both platform names ("Lake Platform 1") and junction
// routing markers ("Smallbrook Junction Line 1") used for the in-game "Go Via"
// instruction signs.
func parseRouteMarkersInDoc(exports []tileExport, tileName string, ribbonGUIDByExportIndex map[int]string) []*RouteMarker {
	type prop struct {
		Name  string          `json:"Name"`
		Value json.RawMessage `json:"Value"`
	}
	out := []*RouteMarker{}
	for _, e := range exports {
		// TrackMarkerProperty / TrackMarkerProperty_N / Default__TrackMarkerProperty.
		// Default__ instances DO carry real data here (verified against IoW Lake
		// Platform 1) — don't filter them.
		if !startsWith(e.ObjectName, "TrackMarkerProperty") &&
			!startsWith(e.ObjectName, "Default__TrackMarkerProperty") {
			continue
		}
		ribbonGUID := ribbonGUIDByExportIndex[e.OuterIndex]
		if ribbonGUID == "" {
			continue
		}
		m := &RouteMarker{RibbonGUID: ribbonGUID, TileName: tileName}
		for _, rawProp := range e.Data {
			var p prop
			if err := json.Unmarshal(rawProp, &p); err != nil {
				continue
			}
			switch p.Name {
			case "MarkerName":
				var s string
				if err := json.Unmarshal(p.Value, &s); err == nil {
					m.Name = s
				}
			case "MarkerType":
				var s string
				if err := json.Unmarshal(p.Value, &s); err == nil {
					m.MarkerType = lastSegment(s, "::")
					if m.MarkerType == "" {
						m.MarkerType = s
					}
				}
			case "LineSide":
				var s string
				if err := json.Unmarshal(p.Value, &s); err == nil {
					m.LineSide = lastSegment(s, "::")
					if m.LineSide == "" {
						m.LineSide = s
					}
				}
			case "Location":
				var f float32
				if err := json.Unmarshal(p.Value, &f); err == nil {
					m.Location = f
				}
			case "Start":
				var f float32
				if err := json.Unmarshal(p.Value, &f); err == nil {
					m.Start = f
				}
			case "End":
				var f float32
				if err := json.Unmarshal(p.Value, &f); err == nil {
					m.End = f
				}
			}
		}
		if m.Name == "" {
			continue
		}
		out = append(out, m)
	}
	return out
}

// parseSwitchesInDoc walks every NetworkTurnoutJunction export and pulls out
// its three connected ribbon GUIDs (Ingoing / Outgoing / Turnout) plus the
// junction's own GUID + node GUID + manual-control flag. Position resolution
// happens downstream — the switch sits at the start of OutgoingRibbon.
func parseSwitchesInDoc(exports []tileExport, tileName string, firstListValue func(json.RawMessage) (string, bool)) []*Switch {
	type prop struct {
		Name  string          `json:"Name"`
		Type  string          `json:"$type"`
		Value json.RawMessage `json:"Value"`
	}
	// Read a Connection struct's ribbon GUID. The nesting is:
	// Value: [ { Name: "RibbonGuid", Value: [ { Value: "<guid>" } ] }, ... ]
	connectionRibbonGUID := func(v json.RawMessage) string {
		var inner []json.RawMessage
		if err := json.Unmarshal(v, &inner); err != nil {
			return ""
		}
		for _, sub := range inner {
			var sp prop
			if err := json.Unmarshal(sub, &sp); err != nil {
				continue
			}
			if sp.Name == "RibbonGuid" {
				if g, ok := firstListValue(sp.Value); ok {
					return g
				}
			}
		}
		return ""
	}
	out := []*Switch{}
	for _, e := range exports {
		n := e.ObjectName
		if !startsWith(n, "NetworkTurnoutJunction") {
			continue
		}
		s := &Switch{TileName: tileName}
		for _, rawProp := range e.Data {
			var p prop
			if err := json.Unmarshal(rawProp, &p); err != nil {
				continue
			}
			switch p.Name {
			case "JctGuid":
				if g, ok := firstListValue(p.Value); ok {
					s.JctGUID = g
				}
			case "NodeGuid":
				if g, ok := firstListValue(p.Value); ok {
					s.NodeGUID = g
				}
			case "IngoingRibbonConnection":
				s.IngoingRibbon = connectionRibbonGUID(p.Value)
			case "OutgoingRibbonConnection":
				s.OutgoingRibbon = connectionRibbonGUID(p.Value)
			case "TurnoutRibbonConnection":
				s.TurnoutRibbon = connectionRibbonGUID(p.Value)
			case "bManuallyControlled":
				var b bool
				if err := json.Unmarshal(p.Value, &b); err == nil {
					s.ManuallyControlled = b
				}
			}
		}
		if s.JctGUID == "" && s.NodeGUID == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// parseLinkedPlatformsInDoc walks every export looking for a `LinkedPlatforms`
// property. Each entry inside is a `RouteLocation` struct with a `Name` and
// a nested `Location.RibbonReference` (Guid) + `Location.RibbonLocation`
// (float). Returns one record per entry — the caller dedupes across tiles.
func parseLinkedPlatformsInDoc(exports []tileExport, tileName string, firstListValue func(json.RawMessage) (string, bool)) []*LinkedPlatform {
	type prop struct {
		Name  string          `json:"Name"`
		Type  string          `json:"$type"`
		Value json.RawMessage `json:"Value"`
	}
	out := []*LinkedPlatform{}
	for _, e := range exports {
		if len(e.Data) == 0 {
			continue
		}
		for _, rawProp := range e.Data {
			var p prop
			if err := json.Unmarshal(rawProp, &p); err != nil {
				continue
			}
			if p.Name != "LinkedPlatforms" {
				continue
			}
			// Value is an array of RouteLocation struct entries.
			var entries []struct {
				Value []json.RawMessage `json:"Value"`
			}
			if err := json.Unmarshal(p.Value, &entries); err != nil {
				continue
			}
			for _, entry := range entries {
				lp := &LinkedPlatform{TileName: tileName}
				for _, sub := range entry.Value {
					var sp prop
					if err := json.Unmarshal(sub, &sp); err != nil {
						continue
					}
					switch sp.Name {
					case "Name":
						var s string
						if err := json.Unmarshal(sp.Value, &s); err == nil {
							lp.Name = s
						}
					case "Location":
						// Location is a NetworkRibbonLocation struct: list of
						// inner properties RibbonReference + RibbonLocation.
						var locInner []json.RawMessage
						if err := json.Unmarshal(sp.Value, &locInner); err != nil {
							continue
						}
						for _, locProp := range locInner {
							var lpProp prop
							if err := json.Unmarshal(locProp, &lpProp); err != nil {
								continue
							}
							switch lpProp.Name {
							case "RibbonReference":
								if v, ok := firstListValue(lpProp.Value); ok {
									lp.RibbonGUID = v
								}
							case "RibbonLocation":
								var f float32
								if err := json.Unmarshal(lpProp.Value, &f); err == nil {
									lp.RibbonLocation = f
								}
							}
						}
					}
				}
				if lp.Name != "" && lp.RibbonGUID != "" {
					out = append(out, lp)
				}
			}
		}
	}
	return out
}

// parseSignalsInDoc walks every NetworkRibbon export's NetworkProperties +
// NetworkPropertiesReverseOrder arrays. For each ref, if the target export
// looks like a signal actor (has SignalID + SignalType properties), emit one
// Signal record with the parent ribbon's GUID + the signal's own
// LocationFraction (0..1 along the ribbon).
//
// A signal can appear in multiple ribbons' property arrays (e.g. junction
// signals); we dedupe by SignalID — first occurrence wins, since the parent
// ribbon's geometry is what determines the signal's world position.
func parseSignalsInDoc(exports []tileExport, tileName string, ribbonGUIDByExportIndex map[int]string) []*Signal {
	type prop struct {
		Name  string          `json:"Name"`
		Type  string          `json:"$type"`
		Value json.RawMessage `json:"Value"`
	}
	// Pre-extract signal info per export index — only for exports that look
	// like a signal (have SignalID + SignalType + Location). Saves repeating
	// the property scan per ribbon reference.
	type sigInfo struct {
		ID       string
		Type     string
		Location float32
	}
	signalCache := map[int]sigInfo{}
	for i, e := range exports {
		if len(e.Data) == 0 {
			continue
		}
		var s sigInfo
		hasID, hasLoc := false, false
		for _, rawProp := range e.Data {
			var p prop
			if err := json.Unmarshal(rawProp, &p); err != nil {
				continue
			}
			switch p.Name {
			case "SignalID":
				var v string
				if err := json.Unmarshal(p.Value, &v); err == nil && v != "" {
					s.ID = v
					hasID = true
				}
			case "SignalType":
				var v string
				if err := json.Unmarshal(p.Value, &v); err == nil {
					// EnumPropertyData emits the value as a string like "ESignalType::Shunt"
					if dd := lastSegment(v, "::"); dd != "" {
						s.Type = dd
					} else {
						s.Type = v
					}
				}
			case "Location":
				var f float32
				if err := json.Unmarshal(p.Value, &f); err == nil {
					s.Location = f
					hasLoc = true
				}
			}
		}
		if hasID && hasLoc {
			signalCache[i+1] = s // 1-based export index
		}
	}
	out := []*Signal{}
	seenID := map[string]bool{}
	for i, e := range exports {
		if !containsNetworkRibbon(e.ObjectName) {
			continue
		}
		ribbonGUID := ribbonGUIDByExportIndex[i+1]
		if ribbonGUID == "" {
			continue
		}
		for _, rawProp := range e.Data {
			var p prop
			if err := json.Unmarshal(rawProp, &p); err != nil {
				continue
			}
			if p.Name != "NetworkProperties" && p.Name != "NetworkPropertiesReverseOrder" {
				continue
			}
			var refs []struct {
				Value int `json:"Value"`
			}
			if err := json.Unmarshal(p.Value, &refs); err != nil {
				continue
			}
			for _, r := range refs {
				info, ok := signalCache[r.Value]
				if !ok {
					continue
				}
				if seenID[info.ID] {
					continue
				}
				seenID[info.ID] = true
				out = append(out, &Signal{
					SignalID:         info.ID,
					SignalType:       info.Type,
					RibbonGUID:       ribbonGUID,
					LocationFraction: info.Location,
					TileName:         tileName,
				})
			}
		}
	}
	return out
}

func lastSegment(s, sep string) string {
	idx := -1
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			idx = i
		}
	}
	if idx < 0 {
		return ""
	}
	return s[idx+len(sep):]
}

func containsNetworkRibbon(name string) bool {
	// Export names are like "NetworkRibbon_42"
	if len(name) < 13 {
		return false
	}
	// check for "NetworkRibbon" as a substring
	for i := 0; i+len("NetworkRibbon") <= len(name); i++ {
		if name[i:i+len("NetworkRibbon")] == "NetworkRibbon" {
			return true
		}
	}
	return false
}
