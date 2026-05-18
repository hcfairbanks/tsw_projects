// datatrack_cooked.go — parse RouteTimetableDataTrack uassets.
//
// What's in a DataTrack:
//
// Every TSW6 route timetable comes with one or more sibling
// "DataTrack" uassets — e.g. for Boston Sprinter:
//
//	BPE_HSP46_Timetable_Final_MasterDataTrack.uasset (54 MB)
//	BPE_HSP46_Timetable_Final_Acela_DataTrack.uasset (7.9 MB)
//	BPE_HSP46_Timetable_Final_CSX_DataTrack.uasset   (3.7 KB)
//
// The top-level uasset has a SoftObjectProperty `MasterDataTrack` plus a
// `bHasValidDataTrack` bool; the soft-ref points at the DataTrack file. Per
// service the game uses the DataTrack to know exactly which track to use
// at every junction — i.e. the pre-baked path that we currently approximate
// by ribbon proximity (and get wrong wherever parallel ribbons run close).
//
// Per-service payload (what this parser pulls out):
//
//	ServiceDataTracks: TMap<FName, FStruct>
//	   key = service name (matches Service.Name in the master timetable)
//	   value = {
//	     TrackData: TArray<RouteTimetableTrackData> [
//	       {
//	         Location: { RibbonReference: FGuid, RibbonLocation: float (0..1) }
//	         Time: int64 ticks  (Timespan)
//	         Distance: float    (cumulative cm along the path)
//	         DataType: ETimetableTrackDataType enum:
//	           StopPoint / GoVia / ActionPoint / ReversePoint /
//	           TrackSectionEntry / TrackSectionExit / MultiOccupancy
//	         InstructionIndex: int (index back into Service.Instructions, -1 = N/A)
//	         GoViaIndex: int
//	         Direction: EDirectionOfTravel (Forwards / Backwards)
//	         DirectionOfTravel: (dup of Direction)
//	         SectionId: FGuid
//	         SignalRef: { RibbonReference: FGuid, PropertyReference: FGuid }
//	         RouteIx: int
//	       }, ...
//	     ]
//	     ActionIndices: TArray<int32>
//	   }
//
// The TrackData array is the gold here — it's an ORDERED breadcrumb of
// (ribbon, ribbon-fraction) along the service's exact in-game path. We can
// resolve each entry against the route's ribbon index to get a precise
// polyline, no junction-state interpretation required.

package uasset

import (
	"fmt"
)

// TrackDataEvent is one row in a service's TrackData array.
type TrackDataEvent struct {
	RibbonGUID       string  `json:"ribbon_guid"`       // 32-char lowercase hex
	RibbonLocation   float32 `json:"ribbon_location"`   // 0..1 fraction along the ribbon
	Distance         float32 `json:"distance"`          // cumulative cm from service start
	TimeTicks        int64   `json:"time_ticks,omitempty"`
	DataType         string  `json:"data_type"`         // StopPoint / GoVia / TrackSectionEntry / ...
	Direction        string  `json:"direction,omitempty"`
	InstructionIndex int32   `json:"instruction_index"` // -1 = unrelated to any Service.Instruction
	GoViaIndex       int32   `json:"go_via_index"`
	SectionID        string  `json:"section_id,omitempty"`
	RouteIx          int32   `json:"route_ix,omitempty"`
}

// ServiceTrackData is the per-service value in a DataTrack's ServiceDataTracks map.
type ServiceTrackData struct {
	ServiceName   string           `json:"service_name"`
	TrackData     []TrackDataEvent `json:"track_data"`
	ActionIndices []int32          `json:"action_indices,omitempty"`
}

// DataTrack is one parsed RouteTimetableDataTrack uasset.
type DataTrack struct {
	Source        string                      `json:"source"` // uasset path
	VersionNumber int32                       `json:"version_number"`
	Services      map[string]ServiceTrackData `json:"services"`
}

// ParseCookedDataTrack reads a *DataTrack.uasset/.uexp pair and returns the
// parsed per-service track data. Returns the first parseable
// RouteTimetableDataTrack export, ignoring Default__ class-default objects.
func ParseCookedDataTrack(uassetPath string) (*DataTrack, error) {
	u, err := ReadUmap(uassetPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", uassetPath, err)
	}
	for _, exp := range u.Exports {
		if exp.ObjectName == "" {
			continue
		}
		// Skip Default__ class-default object — it has empty data.
		if len(exp.ObjectName) >= 9 && exp.ObjectName[:9] == "Default__" {
			continue
		}
		pr := u.PropertyReader(exp)
		if pr == nil {
			continue
		}
		dt, err := pr.parseDataTrackExport()
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", uassetPath, err)
		}
		dt.Source = uassetPath
		return dt, nil
	}
	return nil, fmt.Errorf("no parseable DataTrack export in %s", uassetPath)
}

func (r *reader) parseDataTrackExport() (*DataTrack, error) {
	out := &DataTrack{Services: map[string]ServiceTrackData{}}
	for r.remaining() > 8 {
		t, ok := r.readTag()
		if !ok {
			break
		}
		dp := r.p
		switch {
		case t.name == "VersionNumber" && t.ptype == "IntProperty":
			out.VersionNumber = r.i32()
		case t.name == "ServiceDataTracks" && t.ptype == "MapProperty":
			r.parseServiceDataTracksMap(dp, t.size, out.Services)
		}
		if r.p != dp+t.size {
			r.seek(dp + t.size)
		}
	}
	return out, nil
}

// parseServiceDataTracksMap reads the TMap<FName, FStruct> body.
//
// Layout: 4 bytes NumKeysToRemove + 4 bytes NumEntries + count × {key, value}.
// Key = FName (8 bytes). Value = tagless property stream terminated by None.
func (r *reader) parseServiceDataTracksMap(dp, size int, out map[string]ServiceTrackData) {
	end := dp + size
	r.seek(dp + 4) // NumKeysToRemove
	count := int(r.i32())
	for i := 0; i < count && r.p < end; i++ {
		key := r.fname()
		std := r.parseServiceTrackDataValue(end)
		std.ServiceName = key
		out[key] = std
	}
	r.seek(end)
}

// parseServiceTrackDataValue reads one value-struct body: a tagless property
// stream terminated by None or by reaching the map-body limit.
func (r *reader) parseServiceTrackDataValue(limit int) ServiceTrackData {
	out := ServiceTrackData{}
	for r.p < limit {
		t, ok := r.readTag()
		if !ok {
			break
		}
		dp := r.p
		switch {
		case t.name == "TrackData" && t.ptype == "ArrayProperty":
			out.TrackData = r.parseTrackDataArray(dp, t.size)
		case t.name == "ActionIndices" && t.ptype == "ArrayProperty":
			out.ActionIndices = r.parseInt32Array(dp, t.size)
		}
		if r.p != dp+t.size {
			r.seek(dp + t.size)
		}
	}
	return out
}

// parseTrackDataArray reads Array<RouteTimetableTrackData>.
//
// Layout: i32 count + inner FPropertyTag + count × element bodies (tagless,
// each terminated by None).
func (r *reader) parseTrackDataArray(dp, size int) []TrackDataEvent {
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
	arrEnd := r.p + innerTag.size
	out := make([]TrackDataEvent, 0, count)
	for i := 0; i < count && r.p < arrEnd; i++ {
		evt := r.parseTrackDataEntry(arrEnd)
		out = append(out, evt)
	}
	r.seek(end)
	return out
}

func (r *reader) parseTrackDataEntry(limit int) TrackDataEvent {
	out := TrackDataEvent{InstructionIndex: -1, GoViaIndex: -1, RouteIx: -1}
	for r.p < limit {
		t, ok := r.readTag()
		if !ok {
			break
		}
		dp := r.p
		switch {
		case t.name == "Location" && t.ptype == "StructProperty" && t.structType == "NetworkRibbonLocation":
			guid, loc := r.parseNetworkRibbonLocation(dp, t.size)
			// FGuid serialises as 4 little-endian uint32s. The rest of the
			// codebase keys ribbon maps by NormalizeGUID(fmtGUID(...)) — i.e.
			// the uint32-swapped 32-char hex form. Use the same so our
			// breadcrumbs match the ribbon catalog keys produced by the
			// rails builder; a raw hex.EncodeToString gives byte-order
			// garbage that misses every ribbon and falls back to the
			// proximity walker.
			out.RibbonGUID = NormalizeGUID(fmtGUID(guid))
			out.RibbonLocation = loc
		case t.name == "Time" && t.ptype == "StructProperty" && t.structType == "Timespan":
			out.TimeTicks = r.i64()
		case t.name == "Distance" && t.ptype == "FloatProperty":
			out.Distance = r.f32()
		case t.name == "DataType" && t.ptype == "EnumProperty":
			out.DataType = trimEnumPrefix(r.fname(), "ETimetableTrackDataType::")
		case t.name == "Direction" && t.ptype == "EnumProperty":
			out.Direction = trimEnumPrefix(r.fname(), "EDirectionOfTravel::")
		case t.name == "InstructionIndex" && t.ptype == "IntProperty":
			out.InstructionIndex = r.i32()
		case t.name == "GoViaIndex" && t.ptype == "IntProperty":
			out.GoViaIndex = r.i32()
		case t.name == "SectionId" && t.ptype == "StructProperty" && t.structType == "Guid":
			var guid [16]byte
			copy(guid[:], r.d[dp:dp+16])
			out.SectionID = NormalizeGUID(fmtGUID(guid))
		case t.name == "RouteIx" && t.ptype == "IntProperty":
			out.RouteIx = r.i32()
		}
		if r.p != dp+t.size {
			r.seek(dp + t.size)
		}
	}
	return out
}

// parseInt32Array reads a primitive IntProperty array body.
//
// Layout: i32 count + count × int32. (No inner tag — primitive arrays in UE
// serialise the elements raw.)
func (r *reader) parseInt32Array(dp, size int) []int32 {
	end := dp + size
	count := int(r.i32())
	if count == 0 {
		r.seek(end)
		return nil
	}
	out := make([]int32, 0, count)
	for i := 0; i < count && r.p < end; i++ {
		out = append(out, r.i32())
	}
	r.seek(end)
	return out
}

func trimEnumPrefix(s, prefix string) string {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):]
	}
	return s
}
