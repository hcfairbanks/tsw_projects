package tsw

import (
	"log"
	"math"

	"hud-go/internal/config"
)

// ParseTelemetry converts raw TSW subscription data into the flat telemetry
// object the frontend expects. Matches the Node.js telemetryController logic.
func ParseTelemetry(raw map[string]any) map[string]any {
	cfg := config.Get()
	speedFactor, _, units := conversionFactors(cfg)
	tempUnits := cfg.TemperatureUnits

	data := map[string]any{
		"playerPosition":           nil,
		"localTime":                nil,
		"speed":                    0,
		"direction":                0,
		"limit":                    0,
		"isSlipping":               false,
		"powerHandle":              0,
		"incline":                  0,
		"nextSpeedLimit":           0,
		"distanceToNextSpeedLimit": 0,
		"trainBreak":               0,
		"trainBrakeActive":         false,
		"locomotiveBrakeHandle":    0,
		"locomotiveBrakeActive":    false,
		"electricDynamicBrake":     0,
		"electricBrakeActive":      false,
		"isTractionLocked":         false,
		"weather":                  map[string]any{},
		"doorFrontRight":           nil,
		"doorFrontLeft":            nil,
		"reverser":                 nil,
		"distanceUnits":            units,
		"temperatureUnits":         tempUnits,
		"cameraMode":               nil,
		"distanceToSignal":         nil,
		"signalAspectClass":        nil,
		"distanceToStation":        nil,
		"nextStation":              nil,
		"timetableTime":            nil,
		"timetableLabel":           nil,
	}

	entries, ok := raw["Entries"].([]any)
	if !ok {
		// Log once to help debug
		if len(raw) > 0 {
			keys := make([]string, 0, len(raw))
			for k := range raw {
				keys = append(keys, k)
			}
			log.Printf("[Telemetry] No Entries array in data, keys: %v", keys)
		}
		return data
	}

	for _, e := range entries {
		entry, ok := e.(map[string]any)
		if !ok {
			continue
		}
		valid, _ := entry["NodeValid"].(bool)
		if !valid {
			continue
		}
		values, ok := entry["Values"].(map[string]any)
		if !ok || values == nil {
			continue
		}
		path, _ := entry["Path"].(string)

		switch path {
		case "DriverAid.PlayerInfo":
			if geo, ok := values["geoLocation"].(map[string]any); ok {
				lat, _ := geo["latitude"].(float64)
				lng, _ := geo["longitude"].(float64)
				// Filter stale Chatham position
				if lat != 51.380108707397724 || lng != 0.5219243867730494 {
					data["playerPosition"] = map[string]any{"latitude": lat, "longitude": lng}
				}
			}
			if cm, ok := values["cameraMode"].(string); ok {
				data["cameraMode"] = cm
			}
			if csn, ok := values["currentServiceName"].(string); ok {
				data["currentServiceName"] = csn
			}

		case "DriverAid.TrackData":
			if stations, ok := values["stations"].([]any); ok {
				data["_stations"] = stations
			}
			if markers, ok := values["markers"].([]any); ok {
				data["_markers"] = markers
			}

		case "DriverAid.Data":
			if limit, ok := values["speedLimit"].(map[string]any); ok {
				if v, ok := limit["value"].(float64); ok && v < 1e10 {
					data["limit"] = int(math.Round(v * speedFactor))
				}
			}
			if nextLimit, ok := values["nextSpeedLimit"].(map[string]any); ok {
				if v, ok := nextLimit["value"].(float64); ok && v < 1e10 {
					data["nextSpeedLimit"] = int(math.Round(v * speedFactor))
				}
			}
			if dist, ok := values["distanceToNextSpeedLimit"].(float64); ok {
				data["distanceToNextSpeedLimit"] = int(math.Round(dist))
			}
			if grad, ok := values["gradient"].(float64); ok {
				data["incline"] = grad
			}
			if dist, ok := values["distanceToSignal"].(float64); ok {
				data["distanceToSignal"] = int(math.Round(dist))
			}
			if aspect, ok := values["signalAspectClass"].(string); ok {
				data["signalAspectClass"] = aspect
			}

		case "TimeOfDay.Data":
			if t, ok := values["LocalTimeISO8601"].(string); ok {
				data["localTime"] = t
			}

		case "WeatherManager.Data":
			weather := map[string]any{}
			for _, k := range []string{"Temperature", "Cloudiness", "Precipitation", "Wetness", "GroundSnow", "PiledSnow", "FogDensity"} {
				if v, ok := values[k]; ok {
					weather[k] = v
				}
			}
			data["weather"] = weather

		// HUD function endpoints
		case "CurrentDrivableActor.Function.HUD_GetSpeed":
			if v, ok := values["Speed (ms)"].(float64); ok {
				data["speed"] = int(math.Round(v * speedFactor))
			}

		case "CurrentDrivableActor.Function.HUD_GetDirection":
			if dir, ok := values["Direction"].(float64); ok {
				data["direction"] = dir
				isActive, hasActive := values["IsActive"].(bool)
				if hasActive && !isActive {
					data["reverser"] = -1 // Handle removed
				} else if dir < 0 {
					data["reverser"] = 0 // Reverse
				} else if dir == 0 {
					data["reverser"] = 1 // Neutral
				} else {
					data["reverser"] = 2 // Forward
				}
			}

		case "CurrentDrivableActor.Function.HUD_GetPowerHandle":
			if v, ok := values["Power"].(float64); ok {
				rounded := math.Ceil(v)
				if v < 0 {
					rounded = math.Floor(v)
				}
				if isNeg, ok := values["IsNegative"].(bool); ok && isNeg {
					rounded = -math.Abs(rounded)
				}
				data["powerHandle"] = int(rounded)
			}

		case "CurrentDrivableActor.Function.HUD_GetIsSlipping":
			if v, ok := values["IsSlipping"].(bool); ok {
				data["isSlipping"] = v
			}

		case "CurrentDrivableActor.Function.HUD_GetTrainBrakeHandle":
			if v, ok := values["HandlePosition"].(float64); ok {
				data["trainBreak"] = int(math.Round(v * 100))
			}
			if v, ok := values["IsActive"].(bool); ok {
				data["trainBrakeActive"] = v
			}

		case "CurrentDrivableActor.Function.HUD_GetLocomotiveBrakeHandle":
			if v, ok := values["HandlePosition"].(float64); ok {
				data["locomotiveBrakeHandle"] = v
			}
			if v, ok := values["IsActive"].(bool); ok {
				data["locomotiveBrakeActive"] = v
			}

		case "CurrentDrivableActor.Function.HUD_GetElectricBrakeHandle":
			if v, ok := values["HandlePosition"].(float64); ok {
				data["electricDynamicBrake"] = int(math.Round(v * 100))
			}
			if v, ok := values["IsActive"].(bool); ok {
				data["electricBrakeActive"] = v
			}

		case "CurrentDrivableActor.Function.HUD_GetIsTractionLocked":
			if v, ok := values["IsTractionLocked"].(bool); ok {
				data["isTractionLocked"] = v
			}

		// Door status — CurrentDrivableActor takes priority over CurrentFormation
		case "CurrentDrivableActor/PassengerDoor_FR.Function.GetCurrentInputValue":
			if v, ok := values["ReturnValue"].(float64); ok {
				data["doorFrontRight"] = v > 0
			}
		case "CurrentDrivableActor/PassengerDoor_FL.Function.GetCurrentInputValue":
			if v, ok := values["ReturnValue"].(float64); ok {
				data["doorFrontLeft"] = v > 0
			}
		case "CurrentFormation/1/Door_PassengerDoor_BR.Function.GetCurrentOutputValue":
			if v, ok := values["ReturnValue"].(float64); ok {
				if data["doorFrontRight"] == nil {
					data["doorFrontRight"] = v > 0
				}
			}
		case "CurrentFormation/1/Door_PassengerDoor_BL.Function.GetCurrentOutputValue":
			if v, ok := values["ReturnValue"].(float64); ok {
				if data["doorFrontLeft"] == nil {
					data["doorFrontLeft"] = v > 0
				}
			}
		}
	}

	return data
}

func conversionFactors(cfg *config.Config) (speed, dist float64, units string) {
	if cfg.DistanceUnits == "imperial" {
		return 2.23694, 0.0328084, "imperial" // m/s to mph, cm to feet
	}
	return 3.6, 0.01, "metric" // m/s to km/h, cm to meters
}
