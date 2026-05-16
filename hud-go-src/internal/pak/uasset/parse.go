// Package uasset parses the JSON produced by UAssetGUI (tojson VER_UE4_27)
// for TSW6 RouteTimetableDefinition uassets.
//
// Export[0] carries a base64-encoded binary payload of tagged UE4 properties.
// We decode that payload and walk the property stream directly rather than
// relying on UAssetGUI's structured JSON (which stops at RawExport boundaries
// for complex custom classes).
package uasset

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
)

// ---- Public types -----------------------------------------------------------

// ScheduleItem is one row in a service's schedule (stop, loading event, etc.).
type ScheduleItem struct {
	Action          string  `json:"action"`
	Details         string  `json:"details,omitempty"`
	Location        string  `json:"location,omitempty"`
	Time1           string  `json:"time1,omitempty"`
	Time2           string  `json:"time2,omitempty"`
	SortOrder       int     `json:"sort_order"`
	Structure       string  `json:"structure,omitempty"`
	StructureNumber string  `json:"structure_number,omitempty"`
	RibbonGUID      string  `json:"ribbon_guid,omitempty"`
	RibbonLocation  float32 `json:"ribbon_location,omitempty"`
	// Lat/Lng are computed downstream (in the package writer) from the
	// ribbon geometry + route origin via UTM. Zero when the ribbon isn't
	// anchored or wasn't found in the tile index.
	Lat float64 `json:"lat,omitempty"`
	Lng float64 `json:"lng,omitempty"`
}

// Service is one train working extracted from a timetable uasset.
type Service struct {
	Name                  string         `json:"name"`
	Headcode              string         `json:"headcode"`
	FriendlyName          string         `json:"friendly_name"`
	ServiceNumber         string         `json:"service_number,omitempty"`
	LayerName             string         `json:"layer_name,omitempty"`
	ServiceOperator       string         `json:"service_operator,omitempty"`
	Formation             string         `json:"formation,omitempty"`
	EndOfServiceFormation string         `json:"end_of_service_formation,omitempty"`
	MapPointA             string         `json:"map_point_a,omitempty"`
	MapPointB             string         `json:"map_point_b,omitempty"`
	IsPlayerDrivable      bool           `json:"is_player_drivable"`
	IsHidden              bool           `json:"is_hidden"`
	SpawnWithConductor    bool           `json:"spawn_with_conductor"`
	SpawnWithEngineer     bool           `json:"spawn_with_engineer"`
	PlayerDrivableSide    string         `json:"player_drivable_side,omitempty"` // "Front" / "Back"
	Source                string         `json:"source,omitempty"`               // "Timetable", "Scenario", "Training"
	ServiceClass          string         `json:"service_class,omitempty"`
	ServiceType           string         `json:"service_type,omitempty"`
	Description           string         `json:"description,omitempty"`
	StartTime             string         `json:"start_time,omitempty"`
	StopAndLoadCount      int            `json:"stop_and_load_count,omitempty"` // count of Instructions where bIsStopping && InstructionType==LoadUnload
	Schedule              []ScheduleItem `json:"schedule"`

	// Legacy fields kept for CSV output compatibility.
	ServiceName string        `json:"service_name,omitempty"`
	Stops       []ServiceStop `json:"stops,omitempty"`
}

// ServiceStop is a simplified stopping point for CSV output.
type ServiceStop struct {
	StationName   string `json:"station"`
	ArrivalTime   string `json:"arrival"`
	DepartureTime string `json:"departure"`
	Platform      string `json:"platform,omitempty"`
}

// FormationVehicle is one car/locomotive slot in a Formation's consist.
// RailVehicleID is the per-instance GUID that keys into the timetable's
// CompiledRVMap to recover the RVD asset path (vehicle model).
type FormationVehicle struct {
	RailVehicleID   string  `json:"rail_vehicle_id"`
	MaxLengthM      float32 `json:"max_length_m"`
	ExtensionLengthM float32 `json:"extension_length_m"`
	Flipped         bool    `json:"flipped,omitempty"`
}

// Formation is one train configuration referenced by services.
type Formation struct {
	Name            string             `json:"name"`
	SpawnRibbonGUID string             `json:"spawn_ribbon_guid,omitempty"`
	SpawnRibbonLoc  float32            `json:"spawn_ribbon_location,omitempty"`
	Vehicles        []FormationVehicle `json:"vehicles,omitempty"`
}

// Timetable holds all services from one uasset file.
// CompiledRVMap maps each FormationVehicle.RailVehicleID (GUID) to the asset
// path of the RailVehicleDefinition (RVD) that describes its model. Combined
// with the RVDByPath map (populated by the extractor), this gives us accurate
// per-vehicle class names.
type Timetable struct {
	Route         string            `json:"route"`
	SectionName   string            `json:"section_name,omitempty"`
	AssetPath     string            `json:"asset_path"`
	Services      []Service         `json:"services"`
	Formations    []Formation       `json:"formations,omitempty"`
	CompiledRVMap map[string]string `json:"compiled_rv_map,omitempty"` // GUID hex -> RVD asset path
	// CrossPakReferenceName is the asset-mount path TSW uses to reference
	// this timetable's parent route across pak boundaries. Pulled from a
	// NameMap entry of the form "/<X>/RouteDefinition/...". Every TSW
	// timetable references its own RouteDefinition (the game uses it to
	// load the right level), so this is reliable even for cargo / scenario
	// DLC timetables that don't ship route metadata themselves. Empty if
	// no such entry is found.
	CrossPakReferenceName string `json:"cross_pak_reference_name,omitempty"`
	// RouteDisplayName is the canonical user-facing route name pulled from
	// the parent pak's <X>RouteDefinition.uasset (DisplayName.
	// CultureInvariantString). e.g. "WCML South - London Euston to Milton
	// Keynes". Populated by the extractor after parsing; not derived from
	// the timetable binary itself. Empty for cargo DLC timetables (their
	// pak doesn't contain the parent's RouteDefinition).
	RouteDisplayName string `json:"-"`
	// CountryCode is the short country code from the parent
	// RouteDefinition's RouteDetails.Country ("UK", "US", "FR"...).
	// Populated alongside RouteDisplayName.
	CountryCode string `json:"-"`
	// ScenarioDisplayName is the canonical name from the timetable's
	// sibling <X>_Definition.uasset (e.g. "Navigation & Interaction"
	// for TC_Tu01_Timetable). Lets the writer disambiguate tutorials
	// that all share a generic in-asset Service.FriendlyName like
	// "PlayerService". Empty for service-mode timetables that have no
	// scenario Definition.
	ScenarioDisplayName string `json:"-"`
	// ScenarioDescription / ScenarioType / Plaintext name from the
	// Definition. Optional metadata — surfaced in the per-service
	// JSON's `description` field when populated.
	ScenarioDescription string `json:"-"`
	ScenarioType        string `json:"-"`
	ScenarioPlaintext   string `json:"-"`
	// OriginLat/OriginLng come from the route's persistent map WorldSettings
	// (extracted by the extractor, not from the timetable binary itself).
	// Both zero when unknown. See internal/geo for the UTM conversion.
	OriginLat float64 `json:"origin_lat,omitempty"`
	OriginLng float64 `json:"origin_lng,omitempty"`
	// RVDByPath is populated by the extractor after parsing; not serialised to
	// the main JSON output but used internally to resolve consist metadata.
	RVDByPath map[string]*RVD `json:"-"`
	// Ribbons is populated by the extractor from tile .umap files; keyed by
	// RibbonGuid. Used to resolve schedule items' world positions. After
	// extractor.Extract this is the MERGED map (per-route + cross-pak from the
	// global index) so cross-pak schedule references resolve.
	Ribbons map[string]*Ribbon `json:"-"`
	// RouteRibbons holds ONLY the ribbons sourced from this route's own pak.
	// Used by writers that draw the rail network so we don't include unrelated
	// ribbons that the global index contributed (e.g. every other route's
	// network). Empty when the per-route ribbon scan is skipped.
	RouteRibbons map[string]*Ribbon `json:"-"`
	// LinkedPlatforms is the union of every `LinkedPlatforms` RouteLocation
	// entry found on station blueprint actors in the route's tiles. Used to
	// supplement schedule-derived platforms with physically-present ones that
	// no service happens to call at.
	LinkedPlatforms []*LinkedPlatform `json:"-"`
	// Signals is the union of every signal actor referenced by a ribbon's
	// NetworkProperties / NetworkPropertiesReverseOrder array. Position lives
	// on the parent ribbon at the signal's LocationFraction (0..1).
	Signals []*Signal `json:"-"`
	// Switches is the union of every NetworkTurnoutJunction (track switch)
	// found in the route's tiles. Position is the start of OutgoingRibbon.
	Switches []*Switch `json:"-"`
	// CarStopSigns is the union of every CarStopSignProperty sub-export found
	// on a NetworkRibbon in the route's tiles. Each is the in-game "Car Stop"
	// sign (the green ring) at a platform; one per direction per car class.
	// Resolve via parent ribbon's geometry walked to scalar Location.
	CarStopSigns []*CarStopSign `json:"-"`
	// RouteMarkers is the union of every TrackMarkerProperty on a NetworkRibbon —
	// covers Platform markers ("Lake Platform 1") plus junction routing markers
	// ("Smallbrook Junction Line 1") used for the in-game "Go Via" displays.
	RouteMarkers []*RouteMarker `json:"-"`
	// RibbonVertices is the per-ribbon sampled polyline produced by the rails
	// builder (cookedmap.buildRailsFeature). Keys are normalised ribbon GUIDs;
	// values are [lng, lat] pairs in the same order and resolution the rails
	// GeoJSON layer renders. The per-service path-builder slices from this
	// map instead of arc-walking ribbon geometry independently — guarantees
	// the timetable path lies bit-identically on the rendered rail line, with
	// no risk of analytical drift on clothoid / curved sections.
	// Empty when the rails builder hasn't been run yet (legacy code paths).
	RibbonVertices map[string][][2]float64 `json:"-"`
	// ExtractDir is the on-disk path where this timetable's pak was unpacked
	// (typically `<workDir>/<route.Name>`). Stamped by the extractor when
	// Config.KeepWorkDir is set so post-extract steps (e.g. cookedmap rail
	// generation) can re-read the cooked tile binaries. Empty otherwise —
	// the temp dir has already been deleted by the time the timetable is
	// observed.
	ExtractDir string `json:"-"`
}

// ---- Entry point ------------------------------------------------------------

// Parse reads the JSON file produced by UAssetGUI and returns a Timetable.
func Parse(jsonPath, routeName string) (*Timetable, error) {
	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", jsonPath, err)
	}

	var doc struct {
		NameMap []string `json:"NameMap"`
		Exports []struct {
			ObjectName string `json:"ObjectName"`
			Data       any    `json:"Data"`
		} `json:"Exports"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("unmarshalling %s: %w", jsonPath, err)
	}
	if len(doc.Exports) == 0 {
		return nil, fmt.Errorf("no exports in %s", jsonPath)
	}

	// Export[0] is the main RouteTimetableDefinition — Data is a base64 string.
	dataRaw, ok := doc.Exports[0].Data.(string)
	if !ok {
		return nil, fmt.Errorf("export[0].Data is not a base64 string")
	}
	payload, err := base64.StdEncoding.DecodeString(dataRaw)
	if err != nil {
		return nil, fmt.Errorf("decoding base64 payload: %w", err)
	}

	r := newReader(payload, doc.NameMap)
	top, err := r.parseTopLevel()
	if err != nil {
		return nil, fmt.Errorf("parsing binary payload of %s: %w", jsonPath, err)
	}

	// Classify every service based on the timetable file's path: Scenario
	// files live under /Scenarios/, Training/Tutorial files under /Training/,
	// everything else is the main Timetable (service-mode).
	source := classifySource(jsonPath)
	for i := range top.services {
		top.services[i].Source = source
	}

	tt := &Timetable{
		Route:                 routeName,
		SectionName:           top.sectionName,
		AssetPath:             jsonPath,
		Services:              top.services,
		Formations:            top.formations,
		CompiledRVMap:         top.compiledRVMap,
		CrossPakReferenceName: extractCrossPakReferenceName(doc.NameMap),
	}
	return tt, nil
}

// extractCrossPakReferenceName pulls the parent route's asset-mount path
// out of the timetable's NameMap by looking for a reference of the form
// "/<X>/RouteDefinition/<...>". The first segment is the cross-pak name
// TSW uses internally (e.g. "EustonMiltonKeynes" for WCML South,
// "WCMLPresCar" for WCML Preston-Carlisle, "TrainingCentre" for the
// tutorial environment). Returns "" if no such entry is present.
func extractCrossPakReferenceName(nameMap []string) string {
	const marker = "/RouteDefinition/"
	for _, n := range nameMap {
		if len(n) < 2 || n[0] != '/' {
			continue
		}
		slash := strings.IndexByte(n[1:], '/')
		if slash <= 0 {
			continue
		}
		// n is "/X/..."; check the bit after "X" starts with "/RouteDefinition/".
		if strings.HasPrefix(n[1+slash:], marker) {
			return n[1 : 1+slash]
		}
	}
	return ""
}

// classifySource categorises a timetable by its path:
//
//	.../Scenarios/... -> "Scenario"
//	.../Training/...  -> "Training"
//	all others        -> "Timetable"
func classifySource(path string) string {
	p := strings.ReplaceAll(strings.ToLower(path), `\`, `/`)
	switch {
	case strings.Contains(p, "/scenarios/"):
		return "Scenario"
	case strings.Contains(p, "/training/"):
		return "Training"
	default:
		return "Timetable"
	}
}

// ---- Binary reader ----------------------------------------------------------

type reader struct {
	d  []byte
	p  int
	nm []string
}

func newReader(data []byte, nm []string) *reader {
	return &reader{d: data, nm: nm}
}

func (r *reader) remaining() int { return len(r.d) - r.p }
func (r *reader) tell() int      { return r.p }
func (r *reader) seek(p int)     { r.p = p }

func (r *reader) u8() byte {
	v := r.d[r.p]
	r.p++
	return v
}

func (r *reader) i32() int32 {
	v := int32(binary.LittleEndian.Uint32(r.d[r.p:]))
	r.p += 4
	return v
}

func (r *reader) i64() int64 {
	v := int64(binary.LittleEndian.Uint64(r.d[r.p:]))
	r.p += 8
	return v
}

func (r *reader) f32() float32 {
	v := math.Float32frombits(binary.LittleEndian.Uint32(r.d[r.p:]))
	r.p += 4
	return v
}

func (r *reader) skip(n int) { r.p += n }

func (r *reader) fname() string {
	idx := int(r.i32())
	num := int(r.i32())
	if idx < 0 || idx >= len(r.nm) {
		return fmt.Sprintf("?%d", idx)
	}
	s := r.nm[idx]
	if num > 0 {
		return fmt.Sprintf("%s_%d", s, num-1)
	}
	return s
}

func (r *reader) fstr() string {
	n := r.i32()
	if n == 0 {
		return ""
	}
	// Defensive: reject obviously bogus lengths (beyond the buffer).
	if n > 0 {
		if int(n) > r.remaining() {
			r.p = len(r.d)
			return ""
		}
		s := string(r.d[r.p : r.p+int(n)-1])
		r.p += int(n)
		return s
	}
	n = -n
	if int(n)*2 > r.remaining() {
		r.p = len(r.d)
		return ""
	}
	b := r.d[r.p : r.p+int(n)*2]
	r.p += int(n) * 2
	runes := make([]rune, 0, int(n)-1)
	for i := 0; i < (int(n)-1)*2; i += 2 {
		runes = append(runes, rune(binary.LittleEndian.Uint16(b[i:])))
	}
	return string(runes)
}

// ftext reads an FText blob bounded by `size` bytes. Unknown history types
// (or malformed blobs) return "" rather than blowing past the field boundary.
func (r *reader) ftext(size int) string {
	fieldEnd := r.p + size
	defer func() { r.p = fieldEnd }()
	if size < 5 {
		return ""
	}
	r.skip(4) // flags
	history := r.u8()
	switch history {
	case 0xFF: // None
		if r.p >= fieldEnd {
			return ""
		}
		if r.u8() != 0 {
			return r.fstr()
		}
		return ""
	case 0: // Base: namespace, key, source
		r.fstr() // namespace
		r.fstr() // key
		return r.fstr()
	default:
		// ArgumentFormat / other histories we don't decode — skip payload.
		return ""
	}
}

func (r *reader) guid() {
	r.skip(16)
}

// ---- Tag header -------------------------------------------------------------

type tagInfo struct {
	name       string
	ptype      string
	structType string
	innerType  string
	size       int
	boolVal    byte
}

// readTag reads one FPropertyTag header. Returns (tag, ok) where ok==false
// means a "None" sentinel was found (end of property list).
func (r *reader) readTag() (tagInfo, bool) {
	name := r.fname()
	if name == "None" {
		return tagInfo{}, false
	}
	ptype := r.fname()
	size := int(r.i32())
	r.skip(4) // arr_idx

	var t tagInfo
	t.name = name
	t.ptype = ptype
	t.size = size

	switch ptype {
	case "StructProperty":
		t.structType = r.fname()
		r.guid()
	case "BoolProperty":
		t.boolVal = r.u8()
	case "ByteProperty", "EnumProperty":
		t.innerType = r.fname()
	case "ArrayProperty":
		t.innerType = r.fname()
	case "MapProperty":
		r.fname()
		r.fname()
	case "SetProperty":
		t.innerType = r.fname()
	}
	if r.u8() != 0 { // has_guid
		r.guid()
	}
	return t, true
}

// ---- Top-level parser -------------------------------------------------------

type topLevelData struct {
	sectionName   string
	formations    []Formation
	services      []Service
	compiledRVMap map[string]string
}

func (r *reader) parseTopLevel() (topLevelData, error) {
	var out topLevelData
	for r.remaining() > 8 {
		t, ok := r.readTag()
		if !ok {
			break
		}
		dp := r.p
		switch {
		case t.name == "TimetableName" && t.ptype == "TextProperty":
			out.sectionName = r.ftext(t.size)
			r.seek(dp + t.size)
		case t.name == "Formations" && t.ptype == "ArrayProperty":
			out.formations = r.parseFormationsArray(t.size)
		case t.name == "CompiledRVMap" && t.ptype == "MapProperty":
			out.compiledRVMap = r.parseCompiledRVMap(dp, t.size)
		case t.name == "Services" && t.ptype == "ArrayProperty":
			s, err := r.parseServicesArray(t.size)
			if err != nil {
				return out, err
			}
			out.services = s
			// keep walking in case CompiledRVMap lands after Services
			continue
		default:
			r.skip(t.size)
		}
	}
	if out.services == nil {
		return out, fmt.Errorf("Services array not found in payload")
	}
	return out, nil
}

// parseCompiledRVMap decodes the RailVehicleID(GUID) -> RVD asset path map.
// Serialised layout after the tag header (positioned at `dp`, `size` bytes):
//   4 bytes: AllocationFlags (ignored, observed zero)
//   4 bytes: int32 entry count
//   For each entry (28 bytes):
//     16 bytes: FGuid key (RailVehicleID)
//      8 bytes: FName idx+num  (RVD asset path)
//      4 bytes: trailing (observed zero — unknown small value)
func (r *reader) parseCompiledRVMap(dp, size int) map[string]string {
	end := dp + size
	if size < 8 {
		r.seek(end)
		return nil
	}
	r.seek(dp + 4) // skip alloc flags
	count := int(r.i32())
	if count == 0 {
		r.seek(end)
		return nil
	}
	const entrySize = 28
	if r.p+count*entrySize > end {
		r.seek(end)
		return nil
	}
	out := make(map[string]string, count)
	for i := 0; i < count; i++ {
		base := r.p + i*entrySize
		var g [16]byte
		copy(g[:], r.d[base:base+16])
		guid := fmtGUID(g)
		idx := int32(binary.LittleEndian.Uint32(r.d[base+16:]))
		num := int32(binary.LittleEndian.Uint32(r.d[base+20:]))
		var path string
		if int(idx) >= 0 && int(idx) < len(r.nm) {
			path = r.nm[idx]
			if num > 0 {
				path = fmt.Sprintf("%s_%d", path, num-1)
			}
		}
		if guid != "" && path != "" {
			out[guid] = path
		}
	}
	r.seek(end)
	return out
}

// parseFormationsArray walks the top-level Formations ArrayProperty<StructProperty>
// and returns each formation's name + initial spawn ribbon reference (used to
// resolve WAIT FOR SERVICE locations for chain-head services).
func (r *reader) parseFormationsArray(outerSize int) []Formation {
	end := r.p + outerSize
	count := int(r.i32())
	if count == 0 {
		r.seek(end)
		return nil
	}
	innerTag, ok := r.readTag()
	if !ok || innerTag.size == 0 {
		r.seek(end)
		return nil
	}
	arrEnd := r.p + innerTag.size

	forms := make([]Formation, 0, count)
	for i := 0; i < count && r.p < arrEnd; i++ {
		f, next := r.parseFormation(arrEnd)
		forms = append(forms, f)
		r.seek(next)
	}
	r.seek(end)
	return forms
}

func (r *reader) parseFormation(limit int) (Formation, int) {
	var f Formation
	start := r.p
	for r.p < limit {
		t, ok := r.readTag()
		if !ok {
			break
		}
		dp := r.p
		switch {
		case t.name == "Name" && t.ptype == "NameProperty":
			f.Name = r.fname()
		case t.name == "SpawnLocation" && t.ptype == "StructProperty" && t.structType == "NetworkRibbonLocation":
			guid, loc := r.parseNetworkRibbonLocation(dp, t.size)
			f.SpawnRibbonGUID = fmtGUID(guid)
			f.SpawnRibbonLoc = loc
		case t.name == "RailVehicleInfo" && t.ptype == "ArrayProperty":
			f.Vehicles = r.parseRailVehicleInfoArray(dp, t.size)
		default:
			r.seek(dp + t.size)
			continue
		}
		if r.p != dp+t.size {
			r.seek(dp + t.size)
		}
	}
	// Re-scan past the None sentinel so caller can move to the next element.
	next := r.p
	r.seek(start)
	for r.p < limit {
		t, ok := r.readTag()
		if !ok {
			next = r.p
			break
		}
		r.seek(r.p + t.size)
	}
	return f, next
}

// parseRailVehicleInfoArray reads a Formation's RailVehicleInfo property —
// an ArrayProperty<StructProperty> of per-vehicle entries. Each entry gives
// us the per-instance GUID (RailVehicleID) plus the car's length/extension
// and flipped flag. The GUID keys into Timetable.CompiledRVMap to recover
// the vehicle's RVD asset path.
func (r *reader) parseRailVehicleInfoArray(dp, outerSize int) []FormationVehicle {
	end := dp + outerSize
	count := int(r.i32())
	if count == 0 {
		r.seek(end)
		return nil
	}
	innerTag, ok := r.readTag()
	if !ok || innerTag.size == 0 {
		r.seek(end)
		return nil
	}
	arrEnd := r.p + innerTag.size

	vs := make([]FormationVehicle, 0, count)
	for i := 0; i < count && r.p < arrEnd; i++ {
		v, next := r.parseFormationVehicle(arrEnd)
		vs = append(vs, v)
		r.seek(next)
	}
	r.seek(end)
	return vs
}

func (r *reader) parseFormationVehicle(limit int) (FormationVehicle, int) {
	var v FormationVehicle
	start := r.p
	for r.p < limit {
		t, ok := r.readTag()
		if !ok {
			break
		}
		dp := r.p
		switch {
		case t.name == "RailVehicleID" && t.ptype == "StructProperty" && t.structType == "Guid" && t.size >= 16:
			var g [16]byte
			copy(g[:], r.d[dp:dp+16])
			v.RailVehicleID = fmtGUID(g)
		case t.name == "MaxLength" && t.ptype == "StructProperty" && t.structType == "DistanceQuantity" && t.size >= 4:
			v.MaxLengthM = r.f32()
		case t.name == "ExtensionLength" && t.ptype == "StructProperty" && t.structType == "DistanceQuantity" && t.size >= 4:
			v.ExtensionLengthM = r.f32()
		case t.name == "bFlipped" && t.ptype == "BoolProperty":
			v.Flipped = t.boolVal != 0
		default:
			r.seek(dp + t.size)
			continue
		}
		if r.p != dp+t.size {
			r.seek(dp + t.size)
		}
	}
	// Advance past None sentinel
	next := r.p
	r.seek(start)
	for r.p < limit {
		t, ok := r.readTag()
		if !ok {
			next = r.p
			break
		}
		r.seek(r.p + t.size)
	}
	return v, next
}

// parseServicesArray parses the array data that starts with int32 count
// (position is at the count, having already consumed the outer tag header).
func (r *reader) parseServicesArray(outerSize int) ([]Service, error) {
	end := r.p + outerSize
	count := int(r.i32())
	if count == 0 {
		return nil, nil
	}

	// Inner tag for the struct array
	innerTag, ok := r.readTag()
	if !ok || innerTag.size == 0 {
		return nil, fmt.Errorf("missing inner tag for Services array")
	}
	arrEnd := r.p + innerTag.size

	services := make([]Service, 0, count)
	for i := 0; i < count && r.p < arrEnd; i++ {
		svc, next := r.parseService(arrEnd)
		services = append(services, svc)
		r.seek(next)
	}

	r.seek(end)
	return services, nil
}

// parseService parses one RouteTimetableServiceDefinition struct.
// Returns the populated service and the position after the None sentinel.
func (r *reader) parseService(limit int) (Service, int) {
	var svc Service
	var startTimeTicks int64
	start := r.p

	for r.p < limit {
		t, ok := r.readTag()
		if !ok {
			break // break so post-loop code (headcode, WAIT FOR SERVICE) can run
		}
		dp := r.p

		switch {
		case t.name == "Name" && t.ptype == "NameProperty":
			svc.Name = r.fname()
		case t.name == "LayerName" && t.ptype == "NameProperty":
			svc.LayerName = r.fname()
		case t.name == "ServiceOperator" && t.ptype == "NameProperty":
			svc.ServiceOperator = r.fname()
		case t.name == "FriendlyName" && t.ptype == "TextProperty":
			svc.FriendlyName = r.ftext(t.size)
		case t.name == "ServiceNumber" && t.ptype == "StrProperty":
			svc.ServiceNumber = r.fstr()
		case t.name == "FormationName" && t.ptype == "NameProperty":
			svc.Formation = r.fname()
		case t.name == "EndOfServiceFormationName" && t.ptype == "NameProperty":
			svc.EndOfServiceFormation = r.fname()
		case t.name == "MapPointA" && t.ptype == "StrProperty":
			svc.MapPointA = r.fstr()
		case t.name == "MapPointB" && t.ptype == "StrProperty":
			svc.MapPointB = r.fstr()
		case t.name == "bIsPlayerDrivable" && t.ptype == "BoolProperty":
			svc.IsPlayerDrivable = t.boolVal != 0
		case t.name == "bIsHiddenInFrontend" && t.ptype == "BoolProperty":
			svc.IsHidden = t.boolVal != 0
		case t.name == "bSpawnWithConductor" && t.ptype == "BoolProperty":
			svc.SpawnWithConductor = t.boolVal != 0
		case t.name == "bSpawnWithEngineer" && t.ptype == "BoolProperty":
			svc.SpawnWithEngineer = t.boolVal != 0
		case t.name == "PlayerDrivableSide" && t.ptype == "EnumProperty":
			// ESide::Front -> player drives from vehicle index 0
			// ESide::Back  -> player drives from vehicle at last index
			svc.PlayerDrivableSide = strings.TrimPrefix(r.fname(), "ESide::")
		case t.name == "ServiceClass" && t.ptype == "EnumProperty":
			raw := r.fname()
			svc.ServiceClass = strings.TrimPrefix(raw, "EServiceClass::")
			svc.ServiceType = serviceClassToType(svc.ServiceClass)
		case t.name == "Description" && t.ptype == "TextProperty":
			svc.Description = r.ftext(t.size)
		case t.name == "StartTime" && t.ptype == "StructProperty" && t.structType == "Timespan":
			startTimeTicks = r.i64()
			svc.StartTime = ticksToHMS(startTimeTicks)
		case t.name == "Instructions" && t.ptype == "ArrayProperty":
			svc.Schedule, svc.StopAndLoadCount = r.parseInstructionsArray(dp, t.size)
		default:
			// Same debug as parseInstructionItem — surface unknown service-
			// level properties.
			if os.Getenv("TSW6_DEBUG_INSTR") == "1" {
				fmt.Fprintf(os.Stderr, "[unknown-svc-prop] svc=%s name=%s type=%s structType=%s size=%d\n", svc.Name, t.name, t.ptype, t.structType, t.size)
			}
			r.seek(dp + t.size)
			continue
		}
		// Verify we consumed exactly t.size bytes; if not, jump to end of field
		if r.p != dp+t.size {
			r.seek(dp + t.size)
		}
	}

	// Headcode from FriendlyName: "2U02 : Ryde St. Johns - Ryde Pier Head"
	if idx := strings.Index(svc.FriendlyName, " : "); idx >= 0 {
		svc.Headcode = svc.FriendlyName[:idx]
	} else {
		svc.Headcode = svc.Name
	}
	svc.ServiceName = svc.FriendlyName

	// Fix the first LOAD PASSENGERS time. The spawn-platform LoadUnload stores
	// its time field as a duration-from-service-start rather than an absolute
	// clock time (unlike every subsequent LoadUnload). buildSchedule emits it
	// raw, so we see values like "00:14:45" for a service that starts at 23:18.
	// Rebase to startTime so the first LOAD is consistent with the rest of the
	// schedule (a clock time in the same range as the other rows).
	if startTimeTicks > 0 && len(svc.Schedule) > 0 {
		first := &svc.Schedule[0]
		if first.Action == "LOAD PASSENGERS" && first.Time1 != "" {
			startSecs := startTimeTicks / 10_000_000
			if durSecs := parseHMSSeconds(first.Time1); durSecs >= 0 && durSecs < startSecs {
				// Small value — treat as a duration from service start.
				// Cap to a sensible dwell (60s) rather than the raw encoded
				// offset (often equals the *next* stop's arrival offset, which
				// is clearly wrong for a spawn-platform load time). This keeps
				// first-station departure close to the actual start time.
				const maxDwellSecs = int64(60)
				dwell := durSecs
				if dwell > maxDwellSecs {
					dwell = maxDwellSecs
				}
				first.Time1 = ticksToHMS(startTimeTicks + dwell*10_000_000)
			}
		}
	}

	// Prepend WAIT FOR SERVICE as first schedule item
	if startTimeTicks > 0 {
		svc.Schedule = prependWaitForService(svc.Schedule, startTimeTicks)
	}

	// Build legacy Stops for CSV output
	svc.Stops = buildStops(svc.Schedule, svc.StartTime)

	// Re-scan to find position just after None sentinel
	next := r.p
	r.seek(start)
	for r.p < limit {
		t, ok := r.readTag()
		if !ok {
			next = r.p
			break
		}
		r.seek(r.p + t.size)
	}
	return svc, next
}

// ---- Instructions array -----------------------------------------------------

type instruction struct {
	instructionType     string
	destination         string
	ribbonGUID          [16]byte
	ribbonLocation      float32
	arrivalTime         int64
	simulatedArrival    int64
	completionTime      int64
	simulatedCompletion int64
	loadingCriteria     []loadingCriterion
	isStopping          bool // bIsStopping — whether this instruction stops at a station (vs passes through)
}

type loadingCriterion struct {
	bLoad     bool
	bUnload   bool
	minTimeSecs int64
}

// parseInstructionsArray returns the schedule rows derived from the
// Instructions array and a count of instructions that both stop and load
// passengers (bIsStopping=true AND InstructionType=LoadUnload). That count
// drives the revenue-service check in the conductor_compatible rule.
func (r *reader) parseInstructionsArray(dp, outerSize int) ([]ScheduleItem, int) {
	end := dp + outerSize
	count := int(r.i32())
	if count == 0 {
		r.seek(end)
		return nil, 0
	}
	innerTag, ok := r.readTag()
	if !ok {
		r.seek(end)
		return nil, 0
	}
	arrEnd := r.p + innerTag.size

	instrs := make([]instruction, 0, count)
	for i := 0; i < count && r.p < arrEnd; i++ {
		instr, next := r.parseInstruction(arrEnd)
		instrs = append(instrs, instr)
		r.seek(next)
	}
	r.seek(end)
	stopAndLoad := 0
	for _, ins := range instrs {
		if ins.isStopping && ins.instructionType == "LoadUnload" {
			stopAndLoad++
		}
	}
	return buildSchedule(instrs), stopAndLoad
}

func (r *reader) parseInstruction(limit int) (instruction, int) {
	var instr instruction
	start := r.p

	for r.p < limit {
		t, ok := r.readTag()
		if !ok {
			break
		}
		dp := r.p

		switch {
		case t.name == "InstructionType" && t.ptype == "EnumProperty":
			v := r.fname()
			instr.instructionType = simplifyEnum(v)
		case t.name == "Destination" && t.ptype == "StructProperty":
			instr.destination, instr.ribbonGUID, instr.ribbonLocation = r.parseRouteLocationName(dp, t.size)
		case t.name == "ArrivalTime" && t.ptype == "StructProperty" && t.structType == "Timespan":
			instr.arrivalTime = r.i64()
		case t.name == "SimulatedArrivalTime" && t.ptype == "StructProperty" && t.structType == "Timespan":
			instr.simulatedArrival = r.i64()
		case t.name == "CompletionTime" && t.ptype == "StructProperty" && t.structType == "Timespan":
			instr.completionTime = r.i64()
		case t.name == "SimulatedCompletionTime" && t.ptype == "StructProperty" && t.structType == "Timespan":
			instr.simulatedCompletion = r.i64()
		case t.name == "LoadingCriteria" && t.ptype == "ArrayProperty":
			instr.loadingCriteria = r.parseLoadingCriteriaArray(dp, t.size)
		case t.name == "bIsStopping" && t.ptype == "BoolProperty":
			instr.isStopping = t.boolVal != 0
		default:
			// Debug: surface unknown instruction-level properties so we can
			// find fields the parser is currently dropping (e.g. potential
			// stop-offset / cab-alignment data). Set TSW6_DEBUG_INSTR=1 in
			// the env to enable.
			if os.Getenv("TSW6_DEBUG_INSTR") == "1" {
				fmt.Fprintf(os.Stderr, "[unknown-instr-prop] name=%s type=%s structType=%s size=%d\n", t.name, t.ptype, t.structType, t.size)
			}
			r.seek(dp + t.size)
			continue
		}
		if r.p != dp+t.size {
			r.seek(dp + t.size)
		}
	}

	// Re-scan to find None sentinel
	next := r.p
	r.seek(start)
	for r.p < limit {
		t, ok := r.readTag()
		if !ok {
			next = r.p
			break
		}
		r.seek(r.p + t.size)
	}
	return instr, next
}

func (r *reader) parseRouteLocationName(dp, size int) (name string, guid [16]byte, loc float32) {
	end := dp + size
	for r.p < end {
		t, ok := r.readTag()
		if !ok {
			break
		}
		tdp := r.p
		switch {
		case t.name == "Name" && t.ptype == "NameProperty":
			v := r.fname()
			if v != "None" {
				name = v
			}
		case t.name == "Location" && t.ptype == "StructProperty" && t.structType == "NetworkRibbonLocation":
			guid, loc = r.parseNetworkRibbonLocation(tdp, t.size)
		default:
			if os.Getenv("TSW6_DEBUG_INSTR") == "1" {
				fmt.Fprintf(os.Stderr, "[unknown-dest-prop] name=%s type=%s structType=%s size=%d\n", t.name, t.ptype, t.structType, t.size)
			}
		}
		r.seek(tdp + t.size)
	}
	r.seek(end)
	return
}

func (r *reader) parseNetworkRibbonLocation(dp, size int) (guid [16]byte, loc float32) {
	end := dp + size
	for r.p < end {
		t, ok := r.readTag()
		if !ok {
			break
		}
		tdp := r.p
		switch {
		case t.name == "RibbonReference" && t.ptype == "StructProperty" && t.structType == "Guid":
			copy(guid[:], r.d[tdp:tdp+16])
		case t.name == "RibbonLocation" && t.ptype == "FloatProperty":
			loc = r.f32()
		}
		r.seek(tdp + t.size)
	}
	r.seek(end)
	return
}

func (r *reader) parseLoadingCriteriaArray(dp, outerSize int) []loadingCriterion {
	end := dp + outerSize
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

	criteria := make([]loadingCriterion, 0, count)
	for i := 0; i < count && r.p < arrEnd; i++ {
		crit, next := r.parseLoadingCriterionItem(arrEnd)
		criteria = append(criteria, crit)
		r.seek(next)
	}
	r.seek(end)
	return criteria
}

func (r *reader) parseLoadingCriterionItem(limit int) (loadingCriterion, int) {
	var crit loadingCriterion
	start := r.p

	for r.p < limit {
		t, ok := r.readTag()
		if !ok {
			break
		}
		dp := r.p
		switch {
		case t.name == "bLoad" && t.ptype == "BoolProperty":
			crit.bLoad = t.boolVal != 0
		case t.name == "bUnload" && t.ptype == "BoolProperty":
			crit.bUnload = t.boolVal != 0
		case t.name == "MinimumTime" && t.ptype == "StructProperty" && t.structType == "Timespan":
			ticks := r.i64()
			crit.minTimeSecs = ticks / 10_000_000
		default:
			r.seek(dp + t.size)
			continue
		}
		if r.p != dp+t.size {
			r.seek(dp + t.size)
		}
	}

	// Re-scan for None sentinel
	next := r.p
	r.seek(start)
	for r.p < limit {
		t, ok := r.readTag()
		if !ok {
			next = r.p
			break
		}
		r.seek(r.p + t.size)
	}
	return crit, next
}

// ---- Schedule builder -------------------------------------------------------

func buildSchedule(instrs []instruction) []ScheduleItem {
	var items []ScheduleItem
	seenLoadUnload := false
	i := 0

	for i < len(instrs) {
		instr := instrs[i]
		itype := instr.instructionType

		// Service starts at a platform: leading LoadUnload with no preceding GoTo.
		if itype == "LoadUnload" && !seenLoadUnload {
			lu := instr
			arrTicks := lu.arrivalTime
			if arrTicks <= 0 {
				arrTicks = lu.simulatedArrival
			}
			for _, crit := range lu.loadingCriteria {
				if crit.bLoad {
					loadTime := ""
					if arrTicks > 0 {
						loadTime = ticksToHMS(arrTicks + crit.minTimeSecs*10_000_000)
					}
					items = append(items, ScheduleItem{
						Action:    "LOAD PASSENGERS",
						Time1:     loadTime,
						SortOrder: len(items),
					})
				}
			}
			seenLoadUnload = true
			i++
			continue
		}

		if itype != "GoTo" {
			i++
			continue
		}

		dest := instr.destination
		hasLU := i+1 < len(instrs) && instrs[i+1].instructionType == "LoadUnload"

		if hasLU {
			lu := instrs[i+1]
			arrTicks := lu.arrivalTime
			if arrTicks <= 0 {
				arrTicks = lu.simulatedArrival
			}
			arrTime := ticksToHMS(arrTicks)

			station, structType, structNum := splitLocation(dest)
			details := dest
			if arrTime != "" {
				details = dest + " - " + arrTime
			}

			items = append(items, ScheduleItem{
				Action:          "STOP AT LOCATION",
				Details:         details,
				Location:        station,
				Time1:           arrTime,
				SortOrder:       len(items),
				Structure:       structType,
				StructureNumber: structNum,
				RibbonGUID:      fmtGUID(instr.ribbonGUID),
				RibbonLocation:  instr.ribbonLocation,
			})

			for _, crit := range lu.loadingCriteria {
				if crit.bLoad {
					loadTime := ""
					if arrTicks > 0 {
						loadTime = ticksToHMS(arrTicks + crit.minTimeSecs*10_000_000)
					}
					items = append(items, ScheduleItem{
						Action:    "LOAD PASSENGERS",
						Time1:     loadTime,
						SortOrder: len(items),
					})
				} else if crit.bUnload {
					// Only UNLOAD when not also loading (terminal stop)
					items = append(items, ScheduleItem{
						Action:    "UNLOAD PASSENGERS",
						SortOrder: len(items),
					})
				}
			}
			seenLoadUnload = true
			i += 2
		} else {
			// Bare GoTo: pre-service positioning → STOP AT LOCATION,
			// passing through after first LoadUnload → GO VIA LOCATION.
			action := "STOP AT LOCATION"
			if seenLoadUnload {
				action = "GO VIA LOCATION"
			}
			// Only split on Platform for bare GoTos (not Siding/Line).
			station, structType, structNum := splitPlatformOnly(dest)
			loc := dest
			if structType != "" {
				loc = station
			}
			details := dest
			// TSW renders an unspecified destination on a GO VIA LOCATION
			// row as "As Indicated" in-game (the asset's Name field is the
			// "None" sentinel — see parseRouteLocationName). Mirror that
			// behaviour so the importer / HUD has a useful details string
			// instead of an empty cell. Location stays empty so the HUD's
			// "VIA Location" label still fires for these passthroughs.
			if details == "" && action == "GO VIA LOCATION" {
				details = "As Indicated"
			}
			items = append(items, ScheduleItem{
				Action:          action,
				Details:         details,
				Location:        loc,
				SortOrder:       len(items),
				Structure:       structType,
				StructureNumber: structNum,
				RibbonGUID:      fmtGUID(instr.ribbonGUID),
				RibbonLocation:  instr.ribbonLocation,
			})
			i++
		}
	}
	return items
}

func prependWaitForService(schedule []ScheduleItem, ticks int64) []ScheduleItem {
	s := ticks / 10_000_000
	h := s / 3600
	m := (s % 3600) / 60
	sec := s % 60
	// time2 uses no leading zero for hour (matches reference: "5:20:00")
	t2 := fmt.Sprintf("%d:%02d:%02d", h, m, sec)
	wait := ScheduleItem{
		Action:  "WAIT FOR SERVICE",
		Details: fmt.Sprintf("Service is scheduled to start at %02d:%02d:%02d", h, m, sec),
		Time2:   t2,
	}
	items := make([]ScheduleItem, 0, len(schedule)+1)
	items = append(items, wait)
	for i, item := range schedule {
		item.SortOrder = i + 1
		items = append(items, item)
	}
	return items
}

// buildStops derives the legacy ServiceStop slice from a Schedule for CSV output.
func buildStops(schedule []ScheduleItem, startTime string) []ServiceStop {
	var stops []ServiceStop
	for j, item := range schedule {
		if item.Action != "STOP AT LOCATION" {
			continue
		}
		stop := ServiceStop{
			StationName: item.Location,
			ArrivalTime: item.Time1,
			Platform:    item.StructureNumber,
		}
		// Departure = Time1 of the next LOAD/UNLOAD item if it immediately follows
		if j+1 < len(schedule) {
			next := schedule[j+1]
			if next.Action == "LOAD PASSENGERS" || next.Action == "UNLOAD PASSENGERS" {
				stop.DepartureTime = next.Time1
			}
		}
		stops = append(stops, stop)
	}
	return stops
}

// ---- Helpers ----------------------------------------------------------------

func ticksToHMS(ticks int64) string {
	if ticks <= 0 {
		return ""
	}
	s := ticks / 10_000_000
	h := s / 3600
	m := (s % 3600) / 60
	sec := s % 60
	return fmt.Sprintf("%02d:%02d:%02d", h, m, sec)
}

// parseHMSSeconds parses an "HH:MM:SS" string (as produced by ticksToHMS) back
// into a second count. Returns -1 if the input doesn't match the expected shape.
func parseHMSSeconds(s string) int64 {
	if len(s) != 8 || s[2] != ':' || s[5] != ':' {
		return -1
	}
	var h, m, sec int64
	for i, target := range []*int64{&h, &m, &sec} {
		a, b := i*3, i*3+2
		c1, c2 := s[a], s[b-1]
		if c1 < '0' || c1 > '9' || c2 < '0' || c2 > '9' {
			return -1
		}
		*target = int64(c1-'0')*10 + int64(c2-'0')
	}
	return h*3600 + m*60 + sec
}

// serviceClassToType maps the UE4 EServiceClass enum value to a lowercase
// tag ("passenger" or "freight") suitable for downstream tooling.
func serviceClassToType(class string) string {
	if class == "" {
		return ""
	}
	return strings.ToLower(class)
}

func simplifyEnum(s string) string {
	if idx := strings.LastIndex(s, "::"); idx >= 0 {
		return s[idx+2:]
	}
	return s
}

func splitLocation(loc string) (station, structType, structNum string) {
	// Known structure separators, ordered longest-first so "Boston South Station Track 02"
	// doesn't get caught by "Station" before " Track " matches.
	for _, sep := range []string{" Platform ", " Siding ", " Track ", " Line "} {
		if idx := strings.Index(loc, sep); idx >= 0 {
			return loc[:idx], strings.TrimSpace(sep), loc[idx+len(sep):]
		}
	}
	return loc, "", ""
}

func fmtGUID(g [16]byte) string {
	if g == ([16]byte{}) {
		return ""
	}
	return fmt.Sprintf("%08X-%08X-%08X-%08X",
		binary.LittleEndian.Uint32(g[0:4]),
		binary.LittleEndian.Uint32(g[4:8]),
		binary.LittleEndian.Uint32(g[8:12]),
		binary.LittleEndian.Uint32(g[12:16]))
}

// NormalizeGUID strips non-hex characters and lowercases the result, giving a
// single canonical form so GUIDs produced by fmtGUID (uppercase 8-8-8-8) and
// GUIDs embedded in UAssetGUI umap JSON (any case / separator style) compare
// equal. Returns "" for input that doesn't decode to exactly 32 hex chars.
func NormalizeGUID(s string) string {
	if s == "" {
		return ""
	}
	b := make([]byte, 0, 32)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			b = append(b, c)
		case c >= 'a' && c <= 'f':
			b = append(b, c)
		case c >= 'A' && c <= 'F':
			b = append(b, c+('a'-'A'))
		}
	}
	if len(b) != 32 {
		return ""
	}
	return string(b)
}

// splitPlatformOnly splits on any known structure separator — used for GoTo
// stops. Separated so it can diverge from splitLocation if needed later.
func splitPlatformOnly(loc string) (station, structType, structNum string) {
	return splitLocation(loc)
}
