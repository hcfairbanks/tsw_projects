package output

// CsvDataRow matches the "csvData" row in shareable per-service JSON files.
// Fields use snake_case JSON tags (matches existing import format).
type CsvDataRow struct {
	Action          string `json:"action"`
	CoordSource     any    `json:"coord_source"` // "automatic" for seeded rows, nil otherwise
	Details         string `json:"details"`
	Index           int    `json:"index"`
	Latitude        string `json:"latitude"`
	Location        string `json:"location"`
	Longitude       string `json:"longitude"`
	Structure       string `json:"structure"`
	StructureNumber string `json:"structure_number"`
	Time1           string `json:"time1"`
	Time2           string `json:"time2"`
}

// TimetableRow matches the "timetable" row in shareable per-service JSON files.
// Fields use camelCase for structure / structure_number per the reference format.
type TimetableRow struct {
	Arrival         string `json:"arrival"`
	CoordSource     any    `json:"coord_source"`
	Departure       string `json:"departure"`
	Index           int    `json:"index"`
	Latitude        string `json:"latitude,omitempty"`
	Location        string `json:"location"`
	Longitude       string `json:"longitude,omitempty"`
	Structure       string `json:"structure"`
	StructureNumber string `json:"structure_number"`
}

// ConsistVehicle describes one car slot in a consist. VehicleID is the GUID
// the game reports via the live API (CurrentFormation[i].VehicleID) so the
// HUD can match a running formation back to a service record.
//
// The IsElectric / MaxSpeedKph / MaxPowerKw / Manufacturer / *Description /
// ThumbnailAssetRef / Electrification fields mirror the RVD asset and
// drive the per-class aggregates the Train Classes search page filters on.
// Empty on vehicles whose RVD didn't supply them (drivable=false coach
// cars, etc).
type ConsistVehicle struct {
	VehicleID         string                `json:"vehicle_id"`
	RailVehicleClass  string                `json:"rail_vehicle_class,omitempty"` // e.g. "HSP46"
	FriendlyName      string                `json:"friendly_name,omitempty"`      // e.g. "Rotem CTC-5 MBTA"
	LiveryID          string                `json:"livery_id,omitempty"`
	VehicleCategory   string                `json:"vehicle_category,omitempty"` // Locomotive / PassengerCabCar / etc.
	LengthM           float64               `json:"length_m"`
	IsLead            bool                  `json:"is_lead,omitempty"`
	IsFlipped         bool                  `json:"is_flipped,omitempty"`
	IsElectric        bool                  `json:"is_electric,omitempty"`
	MaxSpeedKph       float32               `json:"max_speed_kph,omitempty"`
	MaxPowerKw        float32               `json:"max_power_kw,omitempty"`
	ManufacturerName  string                `json:"manufacturer_name,omitempty"`
	EngineDescription string                `json:"engine_description,omitempty"`
	TypeDescription   string                `json:"type_description,omitempty"`
	ThumbnailAssetRef string                `json:"thumbnail_asset_ref,omitempty"`
	Electrification   []ElectrificationSpec `json:"electrification,omitempty"`
}

// ElectrificationSpec mirrors uasset.ElectrificationSpec so the package JSON
// surface doesn't need to import the parser types. Same wire format.
type ElectrificationSpec struct {
	Current     string `json:"current,omitempty"`     // "OverheadWires", "ThirdRail", "FourthRail"…
	PickupSide  string `json:"pickup_side,omitempty"`
	VoltageV    int32  `json:"voltage_v,omitempty"`
	FrequencyHz int32  `json:"frequency_hz,omitempty"` // 0 == DC
}

// Consist groups an ordered list of ConsistVehicles with totals.
type Consist struct {
	LengthM  float64          `json:"length_m"`
	CarCount int              `json:"car_count"`
	Vehicles []ConsistVehicle `json:"vehicles"`
}

// FormationClassEntry is one drivable class the player can pick for this
// service (was: TrainClassEntry; renamed 2026-05-10 with the trains→formations
// schema rename).
// `IsDefault` marks the class that matches the formation's actual lead RVD —
// its `Consists` array contains the full per-vehicle detail (including GUIDs).
// Alternative (substitutable) classes have no resolvable GUIDs, so their
// `Consists` array is empty; the HUD can still match the non-lead vehicle IDs
// from the default consist to identify the running service.
type FormationClassEntry struct {
	Class     string    `json:"class"`
	IsDefault bool      `json:"is_default,omitempty"`
	Consists  []Consist `json:"consists"`
}

// AdditionalFormation carries the formation_name + formations[] payload from
// a non-canonical timetable binary that shares this service. When the same
// service is declared in multiple .uasset binaries on the same route (e.g.
// "Boston - Providence Timetable" + "Boston - Providence HSP-46 Timetable"),
// we collapse them into one per-service JSON to avoid the importer's
// (service_name, route_id) dedup silently dropping the second one. The
// canonical pair drives the top-level fields; every other pair's formation
// data lands here so the importer can still link each formation via the
// timetable_formations junction.
//
// Empty / omitted in the common case where a service only appears in one
// binary.
type AdditionalFormation struct {
	FormationName string                `json:"formation_name,omitempty"`
	Formations    []FormationClassEntry `json:"formations,omitempty"`
}

// RouteTrainClass is one entry in `route_<X>.json`'s top-level
// `train_classes` array — the canonical list of every RVD shipped in the
// route's pak set (drivable or not). Importers create one
// `train_classes` DB row per entry, keyed by `RailVehicleClass` (stable
// across AI / player formations, unlike `FriendlyName` which TSW labels
// inconsistently).
//
// Thumbnail bytes ride in the zip at `images/train_classes/<file>.png`;
// `ThumbnailRel` is the path-inside-zip the importer extracts to its
// own static directory. Empty when no thumbnail asset is resolvable.
type RouteTrainClass struct {
	RailVehicleClass  string                `json:"rail_vehicle_class"`        // identity, e.g. "CTC-3"
	FriendlyName      string                `json:"friendly_name,omitempty"`   // display name, e.g. "CTC-3 MBTA"
	LiveryID          string                `json:"livery_id,omitempty"`
	VehicleCategory   string                `json:"vehicle_category,omitempty"`
	Drivable          bool                  `json:"drivable"`
	LengthM           *float32              `json:"length_m,omitempty"`        // approximate_length_m from RVD (already in metres)
	IsElectric        *bool                 `json:"is_electric,omitempty"`
	MaxSpeedKph       *float32              `json:"max_speed_kph,omitempty"`
	MaxPowerKw        *float32              `json:"max_power_kw,omitempty"`
	PoweredAxleCount  *uint32               `json:"powered_axle_count,omitempty"`
	ManufacturerName  string                `json:"manufacturer_name,omitempty"`
	EngineDescription string                `json:"engine_description,omitempty"`
	TypeDescription   string                `json:"type_description,omitempty"`
	ThumbnailRel      string                `json:"thumbnail_rel,omitempty"`   // path-inside-zip, e.g. "images/train_classes/CTC-3.png"
	Electrification   []ElectrificationSpec `json:"electrification,omitempty"`
}

// PackageService is the shareable per-service JSON schema used by the import format.
// Field ordering: identity + metadata up top, bulk arrays (csvData / timetable /
// coordinates / markers) at the bottom for easier inspection.
type PackageService struct {
	// --- identity / taxonomy ---
	ServiceName        string `json:"serviceName"`
	CurrentServiceName string `json:"current_service_name"`
	Description        string `json:"description"`
	ServiceType        string `json:"serviceType"`
	Source             string `json:"source"`
	Playable           bool   `json:"playable"`
	Hidden             bool   `json:"hidden"`
	Bound              any    `json:"bound"`

	// --- location ---
	RouteName    string   `json:"routeName"`
	CountryName  string   `json:"countryName"`
	SectionNames []string `json:"sectionNames"`
	// CrossPakReferenceName is the asset-mount path TSW uses to reference
	// this service's parent route across pak boundaries (e.g.
	// "EustonMiltonKeynes" for WCML South, "WCMLPresCar" for WCML
	// Preston-Carlisle), pulled directly from the timetable asset's
	// NameMap reference to its RouteDefinition. Set on every service.
	// Importers should prefer this over RouteName when associating a
	// timetable with a route — for cargo / scenario DLCs that ship
	// timetables for multiple parent routes in one pak, this is the only
	// authoritative per-file signal that ties each timetable back to the
	// parent it actually runs on.
	CrossPakReferenceName string `json:"cross_pak_reference_name,omitempty"`
	// Route geographic anchor (extracted from pak's persistent map).
	// Used with UTM zone Zone(OriginLng) to convert world-space positions
	// (e.g. ribbon WorldLocation) to real-world lat/lng. See internal/geo.
	OriginLat float64 `json:"origin_lat,omitempty"`
	OriginLng float64 `json:"origin_lng,omitempty"`

	// --- scheduling ---
	StartTime string `json:"startTime"`
	Duration  string `json:"duration"`

	// --- formation / consist ---
	// Ordered array of drivable classes the player can pick for this service.
	// The default class (IsDefault=true) carries the full per-vehicle consist
	// including VehicleID GUIDs for HUD matching against the live API.
	Formations          []FormationClassEntry `json:"formations"`
	ConductorCompatible bool                  `json:"conductorCompatible"`
	// FormationName is the in-pak formation identifier this service runs on
	// (e.g. "Class483_006" or "NB_0535"). HUD's upload pipeline can use this
	// to deterministically link a service to a previously-uploaded formation
	// row, falling back to vehicle-GUID-set matching when names disagree.
	FormationName string `json:"formation_name,omitempty"`
	// AdditionalFormations carries the formation data from sibling timetable
	// binaries that declare this same service (typically a route-specific
	// timetable like "Boston-Providence HSP-46 Timetable" alongside the
	// generic one). Empty when the service appears in only one binary —
	// most services. See AdditionalFormation docs for the merge model.
	AdditionalFormations []AdditionalFormation `json:"additional_formations,omitempty"`

	// --- counters / identifiers ---
	TotalPoints  int `json:"totalPoints"`
	TotalMarkers int `json:"totalMarkers"`

	// --- recording / contributor metadata ---
	RecordingMode          string `json:"recordingMode"`
	Contributor            any    `json:"contributor"`
	CoordinatesContributor any    `json:"coordinates_contributor"`
	CoordinatesSource      string `json:"coordinates_source"`

	// --- bulk arrays ---
	CsvData     []CsvDataRow   `json:"csvData"`
	Timetable   []TimetableRow `json:"timetable"`
	// Coordinates is the train's full physical path along the rail network,
	// resolved by walking the ribbon graph between consecutive schedule
	// waypoints (Dijkstra weighted by ribbon length, then arc-sampled).
	// Format matches hud-go's recording schema (latitude/longitude/height).
	Coordinates []ServiceCoord `json:"coordinates"`
	Markers     []any          `json:"markers"`
}
