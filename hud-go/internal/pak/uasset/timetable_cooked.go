// timetable_cooked.go — read a TSW6 Timetable.uasset directly, no UAssetGUI.
//
// Parse() (the original entry point) takes a UAssetGUI-emitted JSON whose
// Exports[0].Data is a base64-encoded copy of the export's serial body in
// the .uexp file. After base64-decoding it hands the bytes (plus the
// JSON's NameMap) to parseTopLevel — a binary FPropertyTag walker that
// already lives in this package.
//
// ParseCookedTimetable does the same parse without the UAssetGUI hop:
// ReadUmap reads the .uasset/.uexp pair (NameMap + ExportMap), and
// PropertyReader hands us a reader scoped to the export's property
// stream. We feed that into the same parseTopLevel and return an
// identical *Timetable.
//
// Saves ~2-3s of UAssetGUI subprocess startup per timetable; on paks
// with many timetables (Great Western Express ships 600) the saving
// dominates the route extraction time.

package uasset

import (
	"fmt"
)

// ParseCookedTimetable reads a Timetable.uasset (and its .uexp sidecar)
// directly and returns the same *Timetable that Parse(jsonPath, ...)
// would produce after a UAssetGUI conversion. Caller still does
// classifySource via the file path the same way.
func ParseCookedTimetable(uassetPath, routeName string) (*Timetable, error) {
	u, err := ReadUmap(uassetPath)
	if err != nil {
		return nil, fmt.Errorf("read uasset %s: %w", uassetPath, err)
	}
	if len(u.Exports) == 0 {
		return nil, fmt.Errorf("no exports in %s", uassetPath)
	}
	// The main RouteTimetableDefinition export is conventionally Exports[0]
	// (mirrors how UAssetGUI surfaces it). Fall back to the first export
	// whose property stream parses successfully — defensive against any
	// future pak that re-orders the export table.
	var top topLevelData
	var parseErr error
	parsedFromIdx := -1
	for i, exp := range u.Exports {
		pr := u.PropertyReader(exp)
		if pr == nil {
			continue
		}
		t, err := pr.parseTopLevel()
		if err != nil {
			if parseErr == nil {
				parseErr = err
			}
			continue
		}
		top = t
		parsedFromIdx = i
		break
	}
	if parsedFromIdx < 0 {
		if parseErr != nil {
			return nil, fmt.Errorf("parsing %s: %w", uassetPath, parseErr)
		}
		return nil, fmt.Errorf("no parseable RouteTimetableDefinition export in %s", uassetPath)
	}

	// Mirror Parse()'s post-walk classification step so the resulting
	// Timetable is byte-identical to the JSON-roundtrip path.
	source := classifySource(uassetPath)
	for i := range top.services {
		top.services[i].Source = source
	}
	return &Timetable{
		Route:                 routeName,
		SectionName:           top.sectionName,
		AssetPath:             uassetPath,
		Services:              top.services,
		Formations:            top.formations,
		CompiledRVMap:         top.compiledRVMap,
		CrossPakReferenceName: extractCrossPakReferenceName(u.Names),
	}, nil
}
