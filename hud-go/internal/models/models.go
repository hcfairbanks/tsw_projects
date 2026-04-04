package models

// Country represents a country record.
type Country struct {
	ID   int     `db:"id"   json:"id"`
	Name string  `db:"name" json:"name"`
	Code *string `db:"code" json:"code"`
}

// Route represents a train route.
type Route struct {
	ID         int    `db:"id"          json:"id"`
	Name       string `db:"name"        json:"name"`
	CountryID  int    `db:"country_id"  json:"country_id"`
	TswVersion int    `db:"tsw_version" json:"tsw_version"`
}

// Train represents a train/rolling stock.
type Train struct {
	ID   int    `db:"id"   json:"id"`
	Name string `db:"name" json:"name"`
}

// RouteTrain represents a many-to-many link between routes and trains.
type RouteTrain struct {
	ID      int `db:"id"       json:"id"`
	RouteID int `db:"route_id" json:"route_id"`
	TrainID int `db:"train_id" json:"train_id"`
}

// Timetable represents a timetable/service definition.
type Timetable struct {
	ID                     int      `db:"id"                       json:"id"`
	ServiceName            string   `db:"service_name"             json:"service_name"`
	RouteID                *int     `db:"route_id"                 json:"route_id"`
	TrainID                *int     `db:"train_id"                 json:"train_id"`
	ServiceType            string   `db:"service_type"             json:"service_type"`
	Contributor            *string  `db:"contributor"              json:"contributor"`
	CoordinatesContributor *string  `db:"coordinates_contributor"  json:"coordinates_contributor"`
	CreatedAt              *string  `db:"created_at"               json:"created_at"`
	Tonnage                *float64 `db:"tonnage"                  json:"tonnage"`
	CarCount               *int     `db:"car_count"                json:"car_count"`
	TrainLength            *float64 `db:"train_length"             json:"train_length"`
	StartTime              *string  `db:"start_time"               json:"start_time"`
	Duration               *string  `db:"duration"                 json:"duration"`
	ServiceImages          *string  `db:"service_images"           json:"service_images"`
	SectionID              *int     `db:"section_id"               json:"section_id"`
	ConductorCompatible    *int     `db:"conductor_compatible"     json:"conductor_compatible"`
	Bound                  *string  `db:"bound"                    json:"bound"`
	Service                *string  `db:"service"                  json:"service"`
	CurrentServiceName     *string  `db:"current_service_name"     json:"current_service_name"`
}

// TimetableEntry represents a single step within a timetable.
type TimetableEntry struct {
	ID          int     `db:"id"           json:"id"`
	TimetableID int     `db:"timetable_id" json:"timetable_id"`
	ActionID    *int    `db:"action_id"    json:"action_id"`
	Details     *string `db:"details"      json:"details"`
	LocationID  *int    `db:"location_id"  json:"location_id"`
	Platform    *string `db:"platform"     json:"platform"`
	Time1       *string `db:"time1"        json:"time1"`
	Time2       *string `db:"time2"        json:"time2"`
	Latitude    *string `db:"latitude"     json:"latitude"`
	Longitude   *string `db:"longitude"    json:"longitude"`
	ApiName     *string `db:"api_name"     json:"api_name"`
	SortOrder   *int    `db:"sort_order"   json:"sort_order"`
	CoordSource *string `db:"coord_source" json:"coord_source"`
}

// TimetableAction represents a named action (e.g. STOP AT LOCATION).
type TimetableAction struct {
	ID   int    `db:"id"   json:"id"`
	Name string `db:"name" json:"name"`
}

// Location represents a named location on a route.
type Location struct {
	ID      int    `db:"id"       json:"id"`
	RouteID int    `db:"route_id" json:"route_id"`
	Name    string `db:"name"     json:"name"`
}

// Section represents a section of a route.
type Section struct {
	ID      int    `db:"id"       json:"id"`
	RouteID int    `db:"route_id" json:"route_id"`
	Name    string `db:"name"     json:"name"`
}

// SectionTrain links a section to a train.
type SectionTrain struct {
	ID        int `db:"id"         json:"id"`
	SectionID int `db:"section_id" json:"section_id"`
	TrainID   int `db:"train_id"   json:"train_id"`
}

// TimetableTrain links a timetable to a train.
type TimetableTrain struct {
	ID          int `db:"id"           json:"id"`
	TimetableID int `db:"timetable_id" json:"timetable_id"`
	TrainID     int `db:"train_id"     json:"train_id"`
}

// TimetableSection links a timetable to a section.
type TimetableSection struct {
	ID          int `db:"id"           json:"id"`
	TimetableID int `db:"timetable_id" json:"timetable_id"`
	SectionID   int `db:"section_id"   json:"section_id"`
}

// StationNameMapping maps a display name to an API name for a route.
type StationNameMapping struct {
	ID          int     `db:"id"           json:"id"`
	RouteID     *int    `db:"route_id"     json:"route_id"`
	DisplayName string  `db:"display_name" json:"display_name"`
	ApiName     string  `db:"api_name"     json:"api_name"`
	CreatedAt   *string `db:"created_at"   json:"created_at"`
}

// WeatherPreset represents a saved weather configuration.
type WeatherPreset struct {
	ID            int      `db:"id"            json:"id"`
	Name          string   `db:"name"          json:"name"`
	Temperature   float64  `db:"temperature"   json:"temperature"`
	Cloudiness    float64  `db:"cloudiness"    json:"cloudiness"`
	Precipitation float64  `db:"precipitation" json:"precipitation"`
	Wetness       float64  `db:"wetness"       json:"wetness"`
	GroundSnow    float64  `db:"ground_snow"   json:"ground_snow"`
	PiledSnow     float64  `db:"piled_snow"    json:"piled_snow"`
	FogDensity    float64  `db:"fog_density"   json:"fog_density"`
	CreatedAt     *string  `db:"created_at"    json:"created_at"`
}

// TimetableCoordinate stores GPS coordinates for a timetable.
type TimetableCoordinate struct {
	ID          int     `db:"id"           json:"id"`
	TimetableID int     `db:"timetable_id" json:"timetable_id"`
	Coordinates string  `db:"coordinates"  json:"coordinates"`
	CoordSource *string `db:"coord_source" json:"coord_source"`
}

// TimetableMarker represents a station marker for a timetable.
type TimetableMarker struct {
	ID             int      `db:"id"              json:"id"`
	TimetableID    int      `db:"timetable_id"    json:"timetable_id"`
	StationName    string   `db:"station_name"    json:"station_name"`
	MarkerType     *string  `db:"marker_type"     json:"marker_type"`
	Latitude       *float64 `db:"latitude"        json:"latitude"`
	Longitude      *float64 `db:"longitude"       json:"longitude"`
	PlatformLength *float64 `db:"platform_length" json:"platform_length"`
}

// RouteCoordinate stores GPS coordinates for a route.
type RouteCoordinate struct {
	ID          int     `db:"id"          json:"id"`
	RouteID     int     `db:"route_id"    json:"route_id"`
	Coordinates string  `db:"coordinates" json:"coordinates"`
	UpdatedAt   *string `db:"updated_at"  json:"updated_at"`
}

// RouteMarker represents a station marker for a route.
type RouteMarker struct {
	ID             int      `db:"id"              json:"id"`
	RouteID        int      `db:"route_id"        json:"route_id"`
	StationName    string   `db:"station_name"    json:"station_name"`
	MarkerType     *string  `db:"marker_type"     json:"marker_type"`
	Latitude       *float64 `db:"latitude"        json:"latitude"`
	Longitude      *float64 `db:"longitude"       json:"longitude"`
	PlatformLength *float64 `db:"platform_length" json:"platform_length"`
}

// RouteLocation represents a specific location point on a route.
type RouteLocation struct {
	ID         int      `db:"id"          json:"id"`
	RouteID    int      `db:"route_id"    json:"route_id"`
	LocationID *int     `db:"location_id" json:"location_id"`
	Name       string   `db:"name"        json:"name"`
	Bound      *string  `db:"bound"       json:"bound"`
	Platform   *string  `db:"platform"    json:"platform"`
	Latitude   *float64 `db:"latitude"    json:"latitude"`
	Longitude  *float64 `db:"longitude"   json:"longitude"`
}
