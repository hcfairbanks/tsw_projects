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
type ConsistVehicle struct {
	VehicleID        string  `json:"vehicle_id"`
	RailVehicleClass string  `json:"rail_vehicle_class,omitempty"` // e.g. "HSP46"
	FriendlyName     string  `json:"friendly_name,omitempty"`      // e.g. "Rotem CTC-5 MBTA"
	LiveryID         string  `json:"livery_id,omitempty"`
	VehicleCategory  string  `json:"vehicle_category,omitempty"` // Locomotive / PassengerCabCar / etc.
	LengthM          float64 `json:"length_m"`
	IsLead           bool    `json:"is_lead,omitempty"`
	IsFlipped        bool    `json:"is_flipped,omitempty"`
}

// Consist groups an ordered list of ConsistVehicles with totals.
type Consist struct {
	LengthM  float64          `json:"length_m"`
	CarCount int              `json:"car_count"`
	Vehicles []ConsistVehicle `json:"vehicles"`
}

// TrainClassEntry is one drivable class the player can pick for this service.
// `IsDefault` marks the class that matches the formation's actual lead RVD —
// its `Consists` array contains the full per-vehicle detail (including GUIDs).
// Alternative (substitutable) classes have no resolvable GUIDs, so their
// `Consists` array is empty; the HUD can still match the non-lead vehicle IDs
// from the default consist to identify the running service.
type TrainClassEntry struct {
	Class     string    `json:"class"`
	IsDefault bool      `json:"is_default,omitempty"`
	Consists  []Consist `json:"consists"`
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
	// Route geographic anchor (extracted from pak's persistent map).
	// Used with UTM zone Zone(OriginLng) to convert world-space positions
	// (e.g. ribbon WorldLocation) to real-world lat/lng. See internal/geo.
	OriginLat float64 `json:"origin_lat,omitempty"`
	OriginLng float64 `json:"origin_lng,omitempty"`

	// --- scheduling ---
	StartTime string `json:"startTime"`
	Duration  string `json:"duration"`

	// --- train / consist ---
	// Ordered array of drivable classes the player can pick for this service.
	// The default class (IsDefault=true) carries the full per-vehicle consist
	// including VehicleID GUIDs for HUD matching against the live API.
	Trains              []TrainClassEntry `json:"trains"`
	ConductorCompatible bool              `json:"conductorCompatible"`
	// FormationName is the in-pak formation identifier this service runs on
	// (e.g. "Class483_006" or "NB_0535"). HUD's upload pipeline can use this
	// to deterministically link a service to a previously-uploaded train
	// row, falling back to vehicle-GUID-set matching when names disagree.
	FormationName string `json:"formation_name,omitempty"`

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
