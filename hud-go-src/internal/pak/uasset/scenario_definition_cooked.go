package uasset

import (
	"fmt"
	"strings"
)

// ParseCookedScenarioDefinition reads a *_Definition.uasset (scenario or
// tutorial) directly and returns the same *ScenarioDefinition as
// ParseScenarioDefinition(jsonPath). Walks the main export's property
// stream via ReadUmap + the existing FPropertyTag walker.
//
// Returns an error matching ParseScenarioDefinition's contract when the
// asset isn't a Definition (no DisplayName property) so callers can
// continue to treat that as "not a definition asset and skip".
func ParseCookedScenarioDefinition(uassetPath string) (*ScenarioDefinition, error) {
	u, err := ReadUmap(uassetPath)
	if err != nil {
		return nil, fmt.Errorf("read uasset %s: %w", uassetPath, err)
	}
	if len(u.Exports) == 0 {
		return nil, fmt.Errorf("no exports in %s", uassetPath)
	}
	out := &ScenarioDefinition{AssetPath: uassetPath}
	sawDisplayName := false
	for _, exp := range u.Exports {
		pr := u.PropertyReader(exp)
		if pr == nil {
			continue
		}
		try := &ScenarioDefinition{AssetPath: uassetPath}
		hasDisplayName := readScenarioDefinitionExport(pr, try)
		if hasDisplayName {
			out = try
			sawDisplayName = true
			break
		}
	}
	if !sawDisplayName {
		return nil, fmt.Errorf("not a Definition asset (no DisplayName) %s", uassetPath)
	}
	return out, nil
}

func readScenarioDefinitionExport(pr *reader, out *ScenarioDefinition) bool {
	defer func() { _ = recover() }()
	sawDisplayName := false
	for pr.remaining() > 8 {
		t, more := pr.readTag()
		if !more {
			break
		}
		dp := pr.p
		switch t.name {
		case "DisplayName":
			if t.ptype == "TextProperty" {
				out.DisplayName = strings.TrimSpace(pr.ftext(t.size))
				sawDisplayName = out.DisplayName != ""
				continue
			}
		case "Description":
			if t.ptype == "TextProperty" {
				out.Description = strings.TrimSpace(pr.ftext(t.size))
				continue
			}
		case "PlaintextName":
			switch t.ptype {
			case "NameProperty":
				out.PlaintextName = pr.fname()
			case "StrProperty":
				out.PlaintextName = pr.fstr()
			}
			pr.seek(dp + t.size)
			continue
		case "ScenarioType":
			// EnumProperty / ByteProperty — the value is an FName like
			// "EScenarioType::Tutorial". Strip the "EnumName::" prefix
			// to match the JSON path's behaviour.
			if t.ptype == "ByteProperty" || t.ptype == "EnumProperty" {
				v := pr.fname()
				if i := strings.LastIndex(v, "::"); i >= 0 {
					v = v[i+2:]
				}
				out.ScenarioType = v
				pr.seek(dp + t.size)
				continue
			}
		case "StartLocationTag":
			switch t.ptype {
			case "NameProperty":
				out.StartLocationTag = pr.fname()
			case "StrProperty":
				out.StartLocationTag = pr.fstr()
			}
			pr.seek(dp + t.size)
			continue
		case "EndLocationTag":
			switch t.ptype {
			case "NameProperty":
				out.EndLocationTag = pr.fname()
			case "StrProperty":
				out.EndLocationTag = pr.fstr()
			}
			pr.seek(dp + t.size)
			continue
		case "MinutesToComplete":
			if t.ptype == "IntProperty" {
				out.MinutesToComplete = int(pr.i32())
				pr.seek(dp + t.size)
				continue
			}
		case "DifficultyRating":
			if t.ptype == "IntProperty" {
				out.DifficultyRating = int(pr.i32())
				pr.seek(dp + t.size)
				continue
			}
		}
		pr.seek(dp + t.size)
	}
	return sawDisplayName
}
