package uasset

import (
	"fmt"
)

// ParseCookedRouteDefinition reads a <X>RouteDefinition.uasset directly
// (no UAssetGUI roundtrip) and returns the same *RouteDefinition that
// ParseRouteDefinition(jsonPath) would. Walks the main export's property
// stream via ReadUmap + the existing FPropertyTag walker.
//
// Returns an error if the asset isn't a RouteDefinition (no RouteDetails
// struct on the main export) — same contract as ParseRouteDefinition,
// so the caller's "skip non-RouteDefinition assets" loop continues to
// work unchanged.
func ParseCookedRouteDefinition(uassetPath string) (*RouteDefinition, error) {
	u, err := ReadUmap(uassetPath)
	if err != nil {
		return nil, fmt.Errorf("read uasset %s: %w", uassetPath, err)
	}
	if len(u.Exports) == 0 {
		return nil, fmt.Errorf("no exports in %s", uassetPath)
	}

	// Walk every export until we find one that looks like a
	// RouteDefinition (has a RouteDetails struct). Most paks have it
	// at Exports[0] but some bundle multiple sub-assets so we don't
	// hard-code the index.
	out := &RouteDefinition{AssetPath: uassetPath}
	sawRouteDetails := false
	for _, exp := range u.Exports {
		pr := u.PropertyReader(exp)
		if pr == nil {
			continue
		}
		// Snapshot — if this export turns out to be the wrong one
		// we discard the partial fill on the next iteration.
		try := &RouteDefinition{AssetPath: uassetPath}
		hasDetails := readRouteDefinitionExport(pr, try)
		if hasDetails {
			out = try
			sawRouteDetails = true
			break
		}
	}
	if !sawRouteDetails {
		return nil, fmt.Errorf("not a RouteDefinition (no RouteDetails) %s", uassetPath)
	}
	out.CrossPakReferenceName = extractCrossPakReferenceName(u.Names)
	return out, nil
}

// readRouteDefinitionExport walks one export's property stream and fills
// in matching fields on `out`. Returns true iff a `RouteDetails` struct
// was seen — that's our discriminator for "this export IS the route
// definition" vs "this is a sub-asset bundled in the same pak".
func readRouteDefinitionExport(pr *reader, out *RouteDefinition) bool {
	defer func() { _ = recover() }()
	sawDetails := false
	for pr.remaining() > 8 {
		t, more := pr.readTag()
		if !more {
			break
		}
		dp := pr.p
		switch t.name {
		case "DisplayName":
			if t.ptype == "TextProperty" {
				out.DisplayName = pr.ftext(t.size)
				continue // ftext already advanced past the field
			}
		case "StatTrackingName":
			switch t.ptype {
			case "NameProperty":
				out.StatTrackingName = pr.fname()
			case "StrProperty":
				out.StatTrackingName = pr.fstr()
			}
			pr.seek(dp + t.size)
			continue
		case "RouteDetails":
			if t.ptype == "StructProperty" {
				// Nested property stream — recurse until "None".
				sawDetails = true
				inner := &reader{d: pr.d[dp : dp+t.size], nm: pr.nm}
				for inner.remaining() > 8 {
					it, im := inner.readTag()
					if !im {
						break
					}
					idp := inner.p
					if it.name == "Country" && it.ptype == "TextProperty" {
						out.CountryCode = inner.ftext(it.size)
						continue
					}
					inner.seek(idp + it.size)
				}
				pr.seek(dp + t.size)
				continue
			}
		}
		pr.seek(dp + t.size)
	}
	return sawDetails
}
