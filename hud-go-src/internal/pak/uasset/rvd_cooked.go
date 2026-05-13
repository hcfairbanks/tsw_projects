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
		case "bIsElectric":
			if t.ptype == "BoolProperty" {
				out.IsElectric = t.boolVal != 0
				matched = true
				continue
			}
		case "MaximumSpeed":
			// StructProperty<SpeedQuantity>, 4-byte body == single float
			// in metres-per-second. Convert to km/h for the schema column.
			if t.ptype == "StructProperty" {
				out.MaxSpeedKph = pr.f32() * 3.6
				matched = true
				pr.seek(dp + t.size)
				continue
			}
		case "PowerOutput":
			// StructProperty<PowerQuantity>, 4-byte body == single float
			// in watts. Convert to kW for the schema column.
			if t.ptype == "StructProperty" {
				out.MaxPowerKw = pr.f32() / 1000.0
				matched = true
				pr.seek(dp + t.size)
				continue
			}
		case "PoweredAxleCount":
			if t.ptype == "UInt32Property" {
				out.PoweredAxleCount = uint32(pr.i32())
				matched = true
				pr.seek(dp + t.size)
				continue
			}
		case "ManufacturerName":
			if t.ptype == "TextProperty" {
				out.ManufacturerName = strings.TrimSpace(pr.ftext(t.size))
				matched = true
				continue
			}
		case "EngineDescription":
			if t.ptype == "TextProperty" {
				out.EngineDescription = strings.TrimSpace(pr.ftext(t.size))
				matched = true
				continue
			}
		case "TypeDescription":
			if t.ptype == "TextProperty" {
				out.TypeDescription = strings.TrimSpace(pr.ftext(t.size))
				matched = true
				continue
			}
		case "ThumbnailTexture":
			// SoftObjectProperty body in cooked TSW6 format:
			//   FName AssetPath  (8 bytes)
			//   FString SubPath  (4 bytes count == 0 in observed data)
			// The FName resolves to a NameMap entry that is the full
			// asset reference path; we store it raw and let the caller
			// resolve against the unpacked tree.
			if t.ptype == "SoftObjectProperty" {
				out.ThumbnailAssetRef = pr.fname()
				matched = true
				pr.seek(dp + t.size)
				continue
			}
		case "ElectrificationRequirements":
			if t.ptype == "ArrayProperty" && t.innerType == "StructProperty" {
				out.Electrification = readElectrificationArray(pr.d[dp:dp+t.size], pr.nm)
				matched = true
				pr.seek(dp + t.size)
				continue
			}
		}
		pr.seek(dp + t.size)
	}
	return matched
}

// readElectrificationArray walks an ArrayProperty<StructProperty> body of
// ElectrificationRequirements and returns one ElectrificationSpec per
// struct element. The array body layout (UE 4.27 FArrayProperty when
// inner type is StructProperty):
//
//   int32   count
//   FName   arrayName    (8 bytes; == "ElectrificationRequirements")
//   FName   arrayType    (8 bytes; == "StructProperty")
//   int32   size         -- total bytes of all struct elements
//   int32   arrayIndex   (0)
//   FName   structName   (8 bytes; TSW writes "ElectrificationMode")
//   16 byte structGuid   (all zero in observed data)
//   1 byte  hasPropertyGuid (0)
//   --- header total: 53 bytes (4 + 8 + 8 + 4 + 4 + 8 + 16 + 1) ---
//
//   <count × tagged-property struct body, each ending with FName "None">
//
// Each struct element is a normal FPropertyTag sequence. We walk it the
// same way readRVDExport walks the top-level export.
func readElectrificationArray(body []byte, nm []string) []ElectrificationSpec {
	defer func() { _ = recover() }()
	r := &reader{d: body, nm: nm}
	count := int(r.i32())
	if count <= 0 || count > 64 {
		return nil
	}
	// Inner FPropertyTag header for the array's element type.
	r.skip(8)  // arrayName FName
	r.skip(8)  // arrayType FName ("StructProperty")
	r.skip(4)  // int32 size (total)
	r.skip(4)  // int32 arrayIndex
	r.skip(8)  // structName FName ("ElectrificationMode")
	r.skip(16) // struct guid
	r.skip(1)  // has-property-guid byte
	out := make([]ElectrificationSpec, 0, count)
	for i := 0; i < count && r.remaining() > 8; i++ {
		spec := ElectrificationSpec{}
		for r.remaining() > 8 {
			t, more := r.readTag()
			if !more {
				break // "None" terminator — end of this struct element
			}
			dp := r.p
			switch t.name {
			case "PowerSource":
				// EnumProperty<EElectricType>, value like
				// "EElectricType::OverheadWires" or "ThirdRail".
				if t.ptype == "ByteProperty" || t.ptype == "EnumProperty" {
					v := r.fname()
					if i := strings.LastIndex(v, "::"); i >= 0 {
						v = v[i+2:]
					}
					spec.Current = v
					r.seek(dp + t.size)
					continue
				}
			case "PickupSide":
				if t.ptype == "ByteProperty" || t.ptype == "EnumProperty" {
					v := r.fname()
					if i := strings.LastIndex(v, "::"); i >= 0 {
						v = v[i+2:]
					}
					spec.PickupSide = v
					r.seek(dp + t.size)
					continue
				}
			case "SupplyType":
				// Nested StructProperty (107 bytes in observed data)
				// that carries the actual Voltage + Frequency tagged
				// fields. Recurse into the body.
				if t.ptype == "StructProperty" {
					readSupplyTypeStruct(r.d[dp:dp+t.size], r.nm, &spec)
					r.seek(dp + t.size)
					continue
				}
			}
			r.seek(dp + t.size)
		}
		out = append(out, spec)
	}
	return out
}

// readSupplyTypeStruct walks the inner SupplyType struct body and pulls
// Voltage + Frequency into the parent spec. The struct body is a normal
// tagged-property list ending with "None"; we don't know the exact
// alignment / preamble (the 107-byte body is bigger than the sum of the
// fields), so we just scan tags forgivingly until None.
func readSupplyTypeStruct(body []byte, nm []string, spec *ElectrificationSpec) {
	defer func() { _ = recover() }()
	r := &reader{d: body, nm: nm}
	for r.remaining() > 8 {
		t, more := r.readTag()
		if !more {
			return
		}
		dp := r.p
		switch t.name {
		case "Voltage":
			if t.ptype == "IntProperty" {
				spec.VoltageV = r.i32()
				r.seek(dp + t.size)
				continue
			}
		case "Frequency":
			if t.ptype == "IntProperty" {
				spec.FrequencyHz = r.i32()
				r.seek(dp + t.size)
				continue
			}
		}
		r.seek(dp + t.size)
	}
}
