package uasset

import (
	"fmt"
	"strings"
)

// ParseCookedRVD reads an RVD_*.uasset directly (no UAssetGUI roundtrip)
// and returns the same *RVD that ParseRVD(jsonPath) would. Walks the
// main export's property stream via ReadUmap + the existing
// FPropertyTag walker.
func ParseCookedRVD(uassetPath string) (*RVD, error) {
	u, err := ReadUmap(uassetPath)
	if err != nil {
		return nil, fmt.Errorf("read uasset %s: %w", uassetPath, err)
	}
	if len(u.Exports) == 0 {
		return nil, fmt.Errorf("no exports in %s", uassetPath)
	}
	out := &RVD{AssetPath: uassetPath}
	// Walk the first export with a non-empty property stream — RVD assets
	// are simple, single-export, but defensive in case future paks bundle
	// extras in the same file.
	for _, exp := range u.Exports {
		pr := u.PropertyReader(exp)
		if pr == nil {
			continue
		}
		if readRVDExport(pr, out) {
			break
		}
	}
	return out, nil
}

// readRVDExport walks one export's property tags, filling matching
// fields on `out`. Returns true if at least one known property was
// matched (so the caller can stop scanning further exports).
func readRVDExport(pr *reader, out *RVD) bool {
	defer func() { _ = recover() }()
	matched := false
	for pr.remaining() > 8 {
		t, more := pr.readTag()
		if !more {
			break
		}
		dp := pr.p
		switch t.name {
		case "RailVehicleClass":
			if t.ptype == "StrProperty" {
				out.RailVehicleClass = pr.fstr()
				matched = true
				pr.seek(dp + t.size)
				continue
			}
		case "ApproximateLength":
			if t.ptype == "FloatProperty" {
				v := pr.f32()
				out.ApproximateLenM = v / 100.0 // cm -> m
				matched = true
				pr.seek(dp + t.size)
				continue
			}
		case "bIsDrivable":
			if t.ptype == "BoolProperty" {
				out.Drivable = t.boolVal != 0
				matched = true
				continue // BoolProperty body is empty
			}
		case "VehicleCategory":
			// EnumProperty / ByteProperty — value is an FName like
			// "EVehicleCategory::Locomotive". Strip the prefix.
			if t.ptype == "ByteProperty" || t.ptype == "EnumProperty" {
				v := pr.fname()
				if i := strings.LastIndex(v, "::"); i >= 0 {
					v = v[i+2:]
				}
				out.VehicleCategory = v
				matched = true
				pr.seek(dp + t.size)
				continue
			}
		case "FriendlyName":
			if t.ptype == "TextProperty" {
				out.FriendlyName = strings.TrimSpace(pr.ftext(t.size))
				matched = true
				continue
			}
		case "LiveryID":
			switch t.ptype {
			case "StrProperty":
				out.LiveryID = pr.fstr()
			case "NameProperty":
				out.LiveryID = pr.fname()
			}
			matched = true
			pr.seek(dp + t.size)
			continue
		case "AvailableGeographicRegions":
			// Stored as a SetProperty<NameProperty> in TSW6's RVD assets
			// (the JSON path's UAssetGUI emits it as a flat Value array
			// of {"Value": "..."} entries, but the binary form is a Set).
			// Body layout (24 bytes for 2 entries):
			//   4 bytes : padding / allocation flags (observed 0)
			//   4 bytes : int32 count
			//   N x 8   : FName entries (idx + number)
			if (t.ptype == "ArrayProperty" || t.ptype == "SetProperty") && t.innerType == "NameProperty" {
				inner := &reader{d: pr.d[dp : dp+t.size], nm: pr.nm}
				inner.skip(4) // padding
				count := int(inner.i32())
				if count >= 0 && count <= 1024 {
					out.Regions = make([]string, 0, count)
					for i := 0; i < count && inner.remaining() >= 8; i++ {
						v := inner.fname()
						if v != "" {
							out.Regions = append(out.Regions, v)
						}
					}
				}
				matched = true
				pr.seek(dp + t.size)
				continue
			}
		case "bIsSubstitutableUnit":
			if t.ptype == "BoolProperty" {
				out.SubstitutableUnit = t.boolVal != 0
				matched = true
				continue
			}
		case "bHasGuardModeControls":
			if t.ptype == "BoolProperty" {
				out.HasGuardControls = t.boolVal != 0
				matched = true
				continue
			}
		case "ServiceTypes":
			if t.ptype == "IntProperty" {
				out.ServiceTypes = int(pr.i32())
				matched = true
				pr.seek(dp + t.size)
				continue
			}
		}
		pr.seek(dp + t.size)
	}
	return matched
}
