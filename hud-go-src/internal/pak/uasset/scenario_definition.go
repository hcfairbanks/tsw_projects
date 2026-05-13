package uasset

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ScenarioDefinition is the subset of <X>_Definition asset fields the
// extractor reads to give a scenario / tutorial timetable a canonical,
// distinguishing name. Sibling to a `<X>_Timetable.uasset` and lives
// in the same on-disk folder.
//
// Without this, multiple tutorials in the same pak (Training Centre
// has 12) collapse onto a single dedup row at import because the
// timetable's own `Service.FriendlyName` is generically "PlayerService"
// for every one of them.
//
// Source: Exports[0].Data on a Tutorial- or Scenario-Definition
// asset (e.g. TC_Tu01_Definition.uasset, CLN_ScA_Definition.uasset).
// Each TextProperty's CultureInvariantString carries the English
// source string baked into the asset, so no .locres lookup is needed
// — same pattern as RouteDefinition.
type ScenarioDefinition struct {
	AssetPath         string `json:"asset_path"`
	DisplayName       string `json:"display_name,omitempty"`        // canonical name, e.g. "Navigation & Interaction"
	PlaintextName     string `json:"plaintext_name,omitempty"`      // short codename, e.g. "OnFootNavigation"
	Description       string `json:"description,omitempty"`         // English description text
	ScenarioType      string `json:"scenario_type,omitempty"`       // EScenarioType enum value: "Tutorial", "Scenario", "Career", …
	StartLocationTag  string `json:"start_location_tag,omitempty"`  // Name property — physical start point
	EndLocationTag    string `json:"end_location_tag,omitempty"`    // Name property — physical end point
	MinutesToComplete int    `json:"minutes_to_complete,omitempty"` // designer's estimate
	DifficultyRating  int    `json:"difficulty_rating,omitempty"`   // 1..3 typically
}

// ParseScenarioDefinition reads a UAssetGUI-emitted JSON for a scenario
// or tutorial Definition asset and returns the fields we need. Returns
// an error if the file isn't a Definition (no DisplayName property);
// callers can treat that as "not a definition asset" and skip.
func ParseScenarioDefinition(jsonPath string) (*ScenarioDefinition, error) {
	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Exports []struct {
			ObjectName string            `json:"ObjectName"`
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

	type propShape struct {
		Name                   string          `json:"Name"`
		Type                   string          `json:"$type"`
		Value                  json.RawMessage `json:"Value"`
		CultureInvariantString string          `json:"CultureInvariantString"`
	}

	out := &ScenarioDefinition{AssetPath: jsonPath}
	sawDisplayName := false
	for _, rawProp := range props {
		var p propShape
		if err := json.Unmarshal(rawProp, &p); err != nil {
			continue
		}
		switch p.Name {
		case "DisplayName":
			out.DisplayName = p.CultureInvariantString
			sawDisplayName = true
		case "Description":
			out.Description = p.CultureInvariantString
		case "PlaintextName":
			var s string
			if err := json.Unmarshal(p.Value, &s); err == nil {
				out.PlaintextName = s
			}
		case "ScenarioType":
			// Enum prop's value comes through as the literal "EScenarioType::Tutorial".
			var s string
			if err := json.Unmarshal(p.Value, &s); err == nil {
				if i := strings.LastIndex(s, "::"); i >= 0 {
					s = s[i+2:]
				}
				out.ScenarioType = s
			}
		case "StartLocationTag":
			var s string
			if err := json.Unmarshal(p.Value, &s); err == nil {
				out.StartLocationTag = s
			}
		case "EndLocationTag":
			var s string
			if err := json.Unmarshal(p.Value, &s); err == nil {
				out.EndLocationTag = s
			}
		case "MinutesToComplete":
			var i int
			if err := json.Unmarshal(p.Value, &i); err == nil {
				out.MinutesToComplete = i
			}
		case "DifficultyRating":
			var i int
			if err := json.Unmarshal(p.Value, &i); err == nil {
				out.DifficultyRating = i
			}
		}
	}
	if !sawDisplayName {
		return nil, fmt.Errorf("not a Definition asset (no DisplayName) %s", jsonPath)
	}
	return out, nil
}
