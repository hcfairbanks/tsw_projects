package output

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hud-go/internal/pak"
	"hud-go/internal/pak/uasset"
)

// WriteTrainDLCZip packages one standalone train-DLC pak's data into a
// shareable zip. The zip contains:
//
//   - train_dlc_<sanitisedCodename>.json — a single `train_classes[]`
//     array in the same shape route_*.json's train_classes section
//     uses, so the importer's ingestRouteTrainClasses path picks it
//     up without modification.
//   - images/train_classes/<sanitised FriendlyName>.png — one PNG per
//     drivable class with a resolvable thumbnail. Filenames match what
//     the catalog scan + the route-zip writer use, so the importer's
//     extractClassThumbnailsFromZip places them in the right spot.
//
// Inputs:
//   - codename: the pak's catalog codename, e.g. "BPEAcela" or
//     "BRClass390". Used to derive the JSON filename.
//   - rvds: the RVD slice from this pak (typically read from pak_rvds
//     via catalog.LoadAllRVDs filtered by pak_path).
//   - thumbsDir: <resourcesDir>/images/train_classes/ — the path where
//     the catalog scan has already rendered the PNGs. We copy them
//     verbatim into the zip rather than re-rendering.
//
// Returns the count of class entries written so the caller can surface
// it back to the user.
func WriteTrainDLCZip(outZip, codename string, rvds []*uasset.RVD, thumbsDir string) (int, error) {
	if err := os.MkdirAll(filepath.Dir(outZip), 0o755); err != nil {
		return 0, fmt.Errorf("mkdir zip parent: %w", err)
	}
	f, err := os.Create(outZip)
	if err != nil {
		return 0, fmt.Errorf("create zip: %w", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()

	// Build train_classes[] entries — same shape rvdToRouteTrainClass
	// produces in cookedmap.CollectTrainClasses, so the importer treats
	// these identically to a route's bundled classes.
	classes := []RouteTrainClass{}
	seen := map[string]bool{}
	for _, r := range rvds {
		if r == nil || r.RailVehicleClass == "" {
			continue
		}
		if seen[r.RailVehicleClass] {
			continue
		}
		seen[r.RailVehicleClass] = true
		tc := RouteTrainClass{
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
		if r.ThumbnailAssetRef != "" {
			fname := r.FriendlyName
			if fname == "" {
				fname = r.RailVehicleClass
			}
			tc.ThumbnailRel = "images/train_classes/" + pak.SanitiseThumbnailName(fname) + ".png"
		}
		if len(r.Electrification) > 0 {
			tc.Electrification = make([]ElectrificationSpec, len(r.Electrification))
			for i, e := range r.Electrification {
				tc.Electrification[i] = ElectrificationSpec{
					Current:     e.Current,
					PickupSide:  e.PickupSide,
					VoltageV:    e.VoltageV,
					FrequencyHz: e.FrequencyHz,
				}
			}
		}
		classes = append(classes, tc)
	}

	// Single top-level JSON. Empty `features` keeps the schema
	// recognisable to the importer's route_*.json shape (which it
	// already ignores when no features are present) and signals
	// "no route metadata here".
	doc := map[string]any{
		"name":          codename,
		"kind":          "train_dlc",
		"train_classes": classes,
	}
	jsonName := "train_dlc_" + sanitiseClassFilename(codename) + ".json"
	hdr := &zip.FileHeader{Name: jsonName, Method: zip.Deflate, Modified: time.Now()}
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return 0, fmt.Errorf("write zip json header: %w", err)
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return 0, fmt.Errorf("encode train_dlc json: %w", err)
	}

	// Copy each class's thumbnail PNG verbatim from disk. Done after
	// the JSON so the zip's central directory order is JSON-first —
	// keeps the importer's "find the manifest" walk fast.
	for _, c := range classes {
		if c.ThumbnailRel == "" {
			continue
		}
		stem := strings.TrimPrefix(c.ThumbnailRel, "images/train_classes/")
		srcPath := filepath.Join(thumbsDir, stem)
		src, err := os.Open(srcPath)
		if err != nil {
			// Missing PNG isn't fatal — the importer handles 404s.
			continue
		}
		zhdr := &zip.FileHeader{Name: c.ThumbnailRel, Method: zip.Deflate, Modified: time.Now()}
		zw2, err := zw.CreateHeader(zhdr)
		if err != nil {
			src.Close()
			return 0, fmt.Errorf("write zip png header: %w", err)
		}
		if _, err := io.Copy(zw2, src); err != nil {
			src.Close()
			return 0, fmt.Errorf("copy png to zip: %w", err)
		}
		src.Close()
	}

	return len(classes), nil
}
