package uasset

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// RVD holds the subset of RailVehicleDefinition asset fields we need for
// labelling formations, deriving train classes, and computing
// conductor_compatible in the output package.
type RVD struct {
	AssetPath         string   `json:"asset_path"`
	RailVehicleClass  string   `json:"rail_vehicle_class,omitempty"` // e.g. "HSP46", "M7", "Acela"
	ApproximateLenM   float32  `json:"approximate_length_m,omitempty"`
	Drivable          bool     `json:"drivable,omitempty"`
	VehicleCategory   string   `json:"vehicle_category,omitempty"` // e.g. "Locomotive", "PassengerCabCar"
	FriendlyName      string   `json:"friendly_name,omitempty"`    // UI-visible label ("Rotem CTC-5 MBTA")
	LiveryID          string   `json:"livery_id,omitempty"`        // operator grouping ("MBTA", "Amtrak")
	Regions           []string `json:"regions,omitempty"`          // AvailableGeographicRegions
	SubstitutableUnit bool     `json:"substitutable_unit,omitempty"`
	HasGuardControls  bool     `json:"has_guard_controls,omitempty"` // bHasGuardModeControls
	ServiceTypes      int      `json:"service_types,omitempty"`      // bitmask: 3 = commuter, 5/6 = intercity

	// IsElectric reflects the asset's `bIsElectric` flag — true when the
	// vehicle draws power from external electrification (catenary / third
	// rail / etc.). False covers diesel, steam, hybrid not-electrified-
	// mode. We don't yet distinguish those subtypes; if needed later, the
	// asset's RailVehicleClass name often leaks it ("BR66" diesel).
	IsElectric bool `json:"is_electric,omitempty"`
	// MaxSpeedKph and MaxPowerKw are decoded from MaximumSpeed
	// (SpeedQuantity struct, 4-byte float) and PowerOutput (PowerQuantity
	// struct, 4-byte float). Stored unscaled; UI scaling is the caller's
	// job. The asset values are believed to be SI internally — m/s and
	// watts respectively — so we convert at parse time.
	MaxSpeedKph float32 `json:"max_speed_kph,omitempty"`
	MaxPowerKw  float32 `json:"max_power_kw,omitempty"`
	// PoweredAxleCount mirrors the UInt32Property of the same name.
	PoweredAxleCount uint32 `json:"powered_axle_count,omitempty"`

	// ManufacturerName, EngineDescription, TypeDescription are TSW's
	// UI-friendly strings; useful for the Train Classes search page.
	ManufacturerName    string `json:"manufacturer_name,omitempty"`
	EngineDescription   string `json:"engine_description,omitempty"`
	TypeDescription     string `json:"type_description,omitempty"`

	// ThumbnailAssetRef is the unresolved SoftObjectProperty package
	// path for the train's thumbnail texture (e.g.
	// "/IOW_BR_Class483/Data/RailVehicleDefinition/FrontEndAssets/TSW_483Loco_DLC_Thumbnail").
	// Caller resolves it against the unpacked tree and extracts the PNG
	// via ExtractTexture2DPNG.
	ThumbnailAssetRef string `json:"thumbnail_asset_ref,omitempty"`

	// Electrification lists every electrification system the vehicle is
	// compatible with. Decoded from the `ElectrificationRequirements`
	// ArrayProperty<StructProperty>. Empty on non-electric vehicles.
	Electrification []ElectrificationSpec `json:"electrification,omitempty"`
}

// ElectrificationSpec is one entry in an RVD's ElectrificationRequirements
// list. Captures the four fields TSW writes on each struct element:
// Current (overhead / third-rail / etc.), PickupSide (top/bottom/etc.),
// Voltage in volts, Frequency in Hz (0 == DC).
type ElectrificationSpec struct {
	Current    string `json:"current,omitempty"`    // "OverheadWires", "ThirdRail", "FourthRail"…
	PickupSide string `json:"pickup_side,omitempty"`
	VoltageV   int32  `json:"voltage_v,omitempty"`
	FrequencyHz int32 `json:"frequency_hz,omitempty"` // 0 == DC
}

// ParseRVD reads the JSON produced by UAssetGUI for an RVD asset file and
// extracts the fields we need. RVD assets are simpler than timetables —
// UAssetGUI parses the tagged properties directly into structured JSON, so
// there's no base64 payload to walk.
func ParseRVD(jsonPath string) (*RVD, error) {
	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Exports []struct {
			ObjectName string           `json:"ObjectName"`
			Data       []json.RawMessage `json:"Data"`
		} `json:"Exports"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", jsonPath, err)
	}
	if len(doc.Exports) == 0 {
		return nil, fmt.Errorf("no exports in %s", jsonPath)
	}
	var props []json.RawMessage
	for _, e := range doc.Exports {
		if len(e.Data) > 0 {
			props = e.Data
			break
		}
	}
	if props == nil {
		return nil, fmt.Errorf("no exports with data in %s", jsonPath)
	}

	// Property shape captures every field we care about; Value is kept as
	// RawMessage so we can selectively decode it per type.
	type propShape struct {
		Name                  string          `json:"Name"`
		Type                  string          `json:"$type"`
		Value                 json.RawMessage `json:"Value"`
		CultureInvariantString string         `json:"CultureInvariantString"`
	}

	out := &RVD{AssetPath: jsonPath}
	for _, rawProp := range props {
		var p propShape
		if err := json.Unmarshal(rawProp, &p); err != nil {
			continue
		}
		switch p.Name {
		case "RailVehicleClass":
			var s string
			if err := json.Unmarshal(p.Value, &s); err == nil {
				out.RailVehicleClass = s
			}
		case "ApproximateLength":
			var f float32
			if err := json.Unmarshal(p.Value, &f); err == nil {
				out.ApproximateLenM = f / 100.0 // cm -> m
			}
		case "bIsDrivable":
			var b bool
			if err := json.Unmarshal(p.Value, &b); err == nil {
				out.Drivable = b
			}
		case "VehicleCategory":
			var s string
			if err := json.Unmarshal(p.Value, &s); err == nil {
				if idx := strings.LastIndex(s, "::"); idx >= 0 {
					out.VehicleCategory = s[idx+2:]
				} else {
					out.VehicleCategory = s
				}
			}
		case "FriendlyName":
			// TextProperty: the user-facing text is stored in
			// CultureInvariantString. Value holds the localisation key.
			if p.CultureInvariantString != "" {
				out.FriendlyName = strings.TrimSpace(p.CultureInvariantString)
			}
		case "LiveryID":
			var s string
			if err := json.Unmarshal(p.Value, &s); err == nil {
				out.LiveryID = s
			}
		case "AvailableGeographicRegions":
			// Array of NameProperty entries: [{"Value":"MBTA"},{"Value":"MBTA2"}]
			var items []struct {
				Value string `json:"Value"`
			}
			if err := json.Unmarshal(p.Value, &items); err == nil {
				out.Regions = make([]string, 0, len(items))
				for _, it := range items {
					if it.Value != "" {
						out.Regions = append(out.Regions, it.Value)
					}
				}
			}
		case "bIsSubstitutableUnit":
			var b bool
			if err := json.Unmarshal(p.Value, &b); err == nil {
				out.SubstitutableUnit = b
			}
		case "bHasGuardModeControls":
			var b bool
			if err := json.Unmarshal(p.Value, &b); err == nil {
				out.HasGuardControls = b
			}
		case "ServiceTypes":
			var n int
			if err := json.Unmarshal(p.Value, &n); err == nil {
				out.ServiceTypes = n
			}
		}
	}
	return out, nil
}
