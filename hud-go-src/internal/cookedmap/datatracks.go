package cookedmap

import (
	"io/fs"
	"path/filepath"
	"strings"

	"hud-go/internal/pak/uasset"
)

// CollectDataTracks walks every workdir for `*DataTrack.uasset` files,
// parses each, and returns the union of every per-service breadcrumb
// path it finds (keyed by Service.Name).
//
// Why all workdirs: like CollectTrainClasses, a single route can be
// composed of multiple paks — the Boston Sprinter master timetable ships
// in BostonProvidenceGameplayPack alongside its MasterDataTrack +
// Acela_DataTrack + CSX_DataTrack files. Operator-specific services
// (Acela / freight) live in their own DataTrack and have to be unioned
// against the master.
//
// Dedup rule: first parse wins (a service shouldn't appear in two
// DataTracks; if it does, the master usually wins by being scanned first).
//
// Returns nil when nothing parses — callers should treat that as "fall
// back to the proximity-based path builder" rather than an error.
func CollectDataTracks(workdirs []string) map[string]uasset.ServiceTrackData {
	out := map[string]uasset.ServiceTrackData{}
	for _, dir := range workdirs {
		if dir == "" {
			continue
		}
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			base := d.Name()
			lower := strings.ToLower(base)
			if !strings.HasSuffix(lower, ".uasset") {
				return nil
			}
			// Match anything that has "DataTrack" in the name AND lives in a
			// "DataTracks" folder (the pak convention). Filtering on path,
			// not just filename, keeps us from grabbing other DataTrack-y
			// assets (e.g. characters / cabinets with unrelated names) that
			// happen to share the suffix.
			if !strings.Contains(strings.ToLower(filepath.Dir(p)), "datatracks") {
				return nil
			}
			if !strings.Contains(base, "DataTrack") {
				return nil
			}
			dt, err := uasset.ParseCookedDataTrack(p)
			if err != nil || dt == nil {
				return nil
			}
			for name, std := range dt.Services {
				if _, exists := out[name]; exists {
					continue
				}
				out[name] = std
			}
			return nil
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
