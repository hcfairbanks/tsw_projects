package uasset

import (
	"encoding/json"
	"fmt"
	"os"
)

// RouteDefinition is the subset of <Codename>RouteDefinition asset fields
// the extractor reads to populate a route's display name + country.
//
// Source: Exports[0].Data of <X>RouteDefinition.uasset (e.g.
// EMKRouteDefinition.uasset inside the WCML South pak). Each
// TextProperty's CultureInvariantString carries the English source
// string baked directly into the asset, so no .locres lookup is needed.
type RouteDefinition struct {
	AssetPath             string `json:"asset_path"`
	StatTrackingName      string `json:"stat_tracking_name,omitempty"`       // stable internal codename, e.g. "WCMLSouth"
	DisplayName           string `json:"display_name,omitempty"`             // canonical name, e.g. "WCML South - London Euston to Milton Keynes"
	CountryCode           string `json:"country_code,omitempty"`             // short code as it appears in the data: "UK", "US", "FR", ...
	CrossPakReferenceName string `json:"cross_pak_reference_name,omitempty"` // pak's own mount, e.g. "EustonMiltonKeynes" — extracted from the asset's NameMap
}

// ParseRouteDefinition reads the JSON produced by UAssetGUI for a
// <Codename>RouteDefinition.uasset and extracts DisplayName +
// RouteDetails.Country. Returns an error if the file isn't a
// RouteDefinition (no RouteDetails struct in Exports[0]) — callers walk
// a directory of *RouteDefinition.uasset candidates and rely on this to
// filter out the level/reward sub-assets that share the basename suffix.
func ParseRouteDefinition(jsonPath string) (*RouteDefinition, error) {
	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		return nil, err
	}
	var doc struct {
		NameMap []string `json:"NameMap"`
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

	out := &RouteDefinition{AssetPath: jsonPath}
	sawRouteDetails := false
	for _, rawProp := range props {
		var p propShape
		if err := json.Unmarshal(rawProp, &p); err != nil {
			continue
		}
		switch p.Name {
		case "DisplayName":
			out.DisplayName = p.CultureInvariantString
		case "StatTrackingName":
			var s string
			if err := json.Unmarshal(p.Value, &s); err == nil {
				out.StatTrackingName = s
			}
		case "RouteDetails":
			sawRouteDetails = true
			// Value is the nested property array. Pull out Country.
			var inner []json.RawMessage
			if err := json.Unmarshal(p.Value, &inner); err == nil {
				for _, ri := range inner {
					var ip propShape
					if err := json.Unmarshal(ri, &ip); err != nil {
						continue
					}
					if ip.Name == "Country" {
						out.CountryCode = ip.CultureInvariantString
					}
				}
			}
		}
	}
	if !sawRouteDetails {
		return nil, fmt.Errorf("not a RouteDefinition (no RouteDetails) %s", jsonPath)
	}
	// Pull the pak's own mount out of the NameMap. The asset itself is
	// referenced from its parent route's mount as "/<X>/RouteDefinition/<Y>"
	// — we use the same regex the cross-pak ref scanner uses.
	out.CrossPakReferenceName = extractCrossPakReferenceName(doc.NameMap)
	return out, nil
}
