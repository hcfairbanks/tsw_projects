package cookedmap

import (
	"io/fs"
	"path/filepath"
	"strings"

	"hud-go/internal/output"
	"hud-go/internal/pak"
	"hud-go/internal/pak/uasset"
)

// CollectTrainClasses walks every workdir for `RVD_*.uasset` files,
// parses each, and returns one `output.RouteTrainClass` per distinct
// `rail_vehicle_class`. Workdirs are the unpacked-pak trees for every
// pak this route is composed of (geometry-owning pak + sibling paks);
// passing every workdir is how we surface multi-pak routes like Boston
// Sprinter, which spreads RVDs across BostonProvidence /
// BostonProvidenceGameplayPack / BPEAcela / etc.
//
// Dedup rule: first parse wins. If two paks ship an RVD with the same
// `rail_vehicle_class`, the later one is skipped. Skips RVDs whose
// parse fails or whose `rail_vehicle_class` is empty (asset-base
// shells, parse errors).
//
// Thumbnail relative paths are pre-set to
// `images/train_classes/<sanitised>.png` so consumers can pack the
// matching PNG into the zip under the same path.
func CollectTrainClasses(workdirs []string) []output.RouteTrainClass {
	seen := map[string]bool{}
	out := []output.RouteTrainClass{}
	for _, dir := range workdirs {
		if dir == "" {
			continue
		}
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			base := d.Name()
			if !strings.HasPrefix(base, "RVD_") || !strings.HasSuffix(strings.ToLower(base), ".uasset") {
				return nil
			}
			rvd, err := uasset.ParseCookedRVD(p)
			if err != nil || rvd == nil {
				return nil
			}
			if rvd.RailVehicleClass == "" {
				return nil
			}
			if seen[rvd.RailVehicleClass] {
				return nil
			}
			seen[rvd.RailVehicleClass] = true
			out = append(out, rvdToRouteTrainClass(rvd))
			return nil
		})
	}
	return out
}

func rvdToRouteTrainClass(r *uasset.RVD) output.RouteTrainClass {
	tc := output.RouteTrainClass{
		RailVehicleClass:  r.RailVehicleClass,
		FriendlyName:      r.FriendlyName,
		LiveryID:          r.LiveryID,
		VehicleCategory:   r.VehicleCategory,
		Drivable:          r.Drivable,
		ManufacturerName:  r.ManufacturerName,
		EngineDescription: r.EngineDescription,
		TypeDescription:   r.TypeDescription,
	}
	if r.ApproximateLenM > 0 {
		v := r.ApproximateLenM
		tc.LengthM = &v
	}
	// Even when IsElectric is false, surface it so consumers can render
	// "diesel/non-electric" labels with confidence.
	{
		v := r.IsElectric
		tc.IsElectric = &v
	}
	if r.MaxSpeedKph > 0 {
		v := r.MaxSpeedKph
		tc.MaxSpeedKph = &v
	}
	if r.MaxPowerKw > 0 {
		v := r.MaxPowerKw
		tc.MaxPowerKw = &v
	}
	if r.PoweredAxleCount > 0 {
		v := r.PoweredAxleCount
		tc.PoweredAxleCount = &v
	}
	// Pre-set the relative path the zip packer will use for this class's
	// thumbnail. Caller is responsible for actually writing the PNG into
	// the zip at this path (see addClassThumbnailsToZip).
	if r.ThumbnailAssetRef != "" {
		tc.ThumbnailRel = "images/train_classes/" + pak.SanitiseThumbnailName(r.RailVehicleClass) + ".png"
	}
	if len(r.Electrification) > 0 {
		tc.Electrification = make([]output.ElectrificationSpec, len(r.Electrification))
		for i, e := range r.Electrification {
			tc.Electrification[i] = output.ElectrificationSpec{
				Current:     e.Current,
				PickupSide:  e.PickupSide,
				VoltageV:    e.VoltageV,
				FrequencyHz: e.FrequencyHz,
			}
		}
	}
	return tc
}
