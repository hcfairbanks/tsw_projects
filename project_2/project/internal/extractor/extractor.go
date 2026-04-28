package extractor

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"tsw6-timetable/internal/pak"
	"tsw6-timetable/internal/pak/uasset"
)

// Extractor orchestrates the full pak → uasset → JSON → struct pipeline.
type Extractor struct {
	cfg Config
}

// New creates a new Extractor with the given configuration.
func New(cfg Config) *Extractor {
	return &Extractor{cfg: cfg}
}

// Extract runs the full extraction pipeline and returns all timetables found.
func (e *Extractor) Extract() ([]*uasset.Timetable, error) {
	routes, err := pak.DiscoverRoutes(e.cfg.TSWPath)
	if err != nil {
		return nil, fmt.Errorf("route discovery: %w", err)
	}

	// Apply route filter
	if e.cfg.RouteFilter != "" {
		filtered := routes[:0]
		for _, r := range routes {
			if strings.Contains(strings.ToLower(r.Name), strings.ToLower(e.cfg.RouteFilter)) {
				filtered = append(filtered, r)
			}
		}
		routes = filtered
	}

	if len(routes) == 0 {
		return nil, nil
	}

	e.logf("Processing %d route(s)...\n", len(routes))

	// Load the global ribbon index if it exists. Ribbons sourced from other
	// paks (e.g. Boston Back Bay's ribbon lives in BostonProvidence even when
	// you're extracting BostonWorcester) get merged into per-route maps so
	// schedule lat/lng resolves across pak boundaries.
	idx, _ := LoadRibbonIndex(DefaultIndexPath())
	if idx != nil && len(idx.Ribbons) > 0 {
		e.logf("Loaded global ribbon index: %d ribbons across %d paks\n", len(idx.Ribbons), len(idx.Paks))
	}

	// Work dir for temporary extracted files
	workDir, err := os.MkdirTemp("", "tsw6-timetable-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}
	if e.cfg.Debug {
		fmt.Fprintf(os.Stderr, "[debug] temp dir: %s\n", workDir)
	} else {
		defer os.RemoveAll(workDir)
	}

	// Pre-pass: extract RVD assets from *every* installed pak so we can resolve
	// train classes for vehicles that live in separate DLC paks (e.g. Acela
	// running on Boston Sprinter, CSX freight cars across multiple routes).
	// Repak supports --include filters, so we only pay for RVDs, not full paks.
	var globalRVDs map[string]*uasset.RVD
	if e.cfg.RepakPath != "" {
		e.logf("Scanning all paks for RVD assets (one-time)...\n")
		globalRVDs, err = e.ScanAllRVDs(workDir)
		if err != nil && e.cfg.Debug {
			fmt.Fprintf(os.Stderr, "[rvd-scan] %v\n", err)
		}
		e.logf("  discovered %d unique RVDs across all paks\n", len(globalRVDs))
	}

	var all []*uasset.Timetable

	for _, route := range routes {
		e.logf("[%s] Extracting pak...\n", route.Name)

		extractDir := filepath.Join(workDir, route.Name)
		if err := os.MkdirAll(extractDir, 0o755); err != nil {
			return nil, err
		}

		// Step 1: Unpack the .pak file
		if err := e.runUnrealPak(route.PakPath, extractDir); err != nil {
			e.logf("[%s] WARNING: UnrealPak failed: %v — skipping\n", route.Name, err)
			continue
		}

		// Step 2: Find timetable uasset files in the extracted output
		uassets, err := findTimetableAssets(extractDir)
		if err != nil || len(uassets) == 0 {
			e.logf("[%s] No timetable assets found\n", route.Name)
			continue
		}

		e.logf("[%s] Found %d timetable asset(s)\n", route.Name, len(uassets))

		// Step 2a: Locate the persistent-level map file and extract the
		// route's origin lat/lng from its uexp. These feed the UTM conversion
		// (see internal/geo) and are attached to every timetable from this route.
		originLat, originLng := e.findRouteOrigin(extractDir, route.Name)

		// Step 2a.5: Walk each tile .umap and index every NetworkRibbon, plus
		// collect LinkedPlatforms (named RouteLocation entries on station
		// blueprints — these supplement schedule-derived platforms with
		// physically-present ones no service calls at). Schedule's ribbon
		// references resolve against the ribbon map to produce real-world
		// lat/lng per schedule item. Skippable via --skip-ribbons for fast
		// iteration on non-geo fields.
		var ribbonMap map[string]*uasset.Ribbon
		var routeOnlyRibbons map[string]*uasset.Ribbon
		var linkedPlatforms []*uasset.LinkedPlatform
		var signals []*uasset.Signal
		var switches []*uasset.Switch
		var carStopSigns []*uasset.CarStopSign
		var routeMarkers []*uasset.RouteMarker
		if !e.cfg.SkipRibbonScan {
			ribbonMap, linkedPlatforms, signals, switches, carStopSigns, routeMarkers = e.scanTileFeatures(extractDir)
			// Snapshot the per-route map BEFORE merging the global index so
			// rails writers can emit just this route's network without the
			// rest of the world.
			if len(ribbonMap) > 0 {
				routeOnlyRibbons = make(map[string]*uasset.Ribbon, len(ribbonMap))
				for k, v := range ribbonMap {
					routeOnlyRibbons[k] = v
				}
			}
		} else if e.cfg.Debug {
			fmt.Fprintf(os.Stderr, "[ribbon-scan] skipped (--skip-ribbons)\n")
		}

		// Merge in the global index. Per-route ribbons win when both exist
		// AND the route's copy is anchored; otherwise the index's anchored
		// copy fills the gap. This is what resolves cross-pak ribbons like
		// Boston Back Bay (defined in BP, referenced from BOW timetables).
		if idx != nil && len(idx.Ribbons) > 0 {
			if ribbonMap == nil {
				ribbonMap = map[string]*uasset.Ribbon{}
			}
			added := 0
			for k, r := range idx.Ribbons {
				existing, ok := ribbonMap[k]
				if !ok {
					ribbonMap[k] = r
					added++
					continue
				}
				if !existing.HasAnchor && r.HasAnchor {
					ribbonMap[k] = r
				}
			}
			if e.cfg.Debug {
				fmt.Fprintf(os.Stderr, "[index] merged %d ribbons from global index into %s map\n", added, route.Name)
			}
		}

		// Step 2b: Find + parse every RailVehicleDefinition (RVD) asset in the
		// route's extracted pak. Overlay those on top of the global (all-paks)
		// RVD map so per-route RVDs win when both exist.
		rvdMap := make(map[string]*uasset.RVD, len(globalRVDs))
		for k, v := range globalRVDs {
			rvdMap[k] = v
		}
		for k, v := range e.scanRVDs(extractDir, route.Name) {
			rvdMap[k] = v
		}

		for _, ua := range uassets {
			// Step 3: Convert uasset → JSON via UAssetGUI CLI
			jsonPath := ua + ".json"
			if err := e.runUAssetGUI(ua, jsonPath); err != nil {
				fmt.Fprintf(os.Stderr, "[%s] WARNING: UAssetGUI failed on %s: %v\n", route.Name, filepath.Base(ua), err)
				continue
			}

			// Step 4: Parse the JSON into our data model
			if e.cfg.Debug {
				fmt.Fprintf(os.Stderr, "[debug] parsing JSON: %s\n", jsonPath)
			}
			tt, err := uasset.Parse(jsonPath, route.Name)
			if err != nil {
				fmt.Fprintf(os.Stderr, "[%s] WARNING: parse failed on %s: %v\n", route.Name, filepath.Base(jsonPath), err)
				continue
			}
			if e.cfg.Debug {
				fmt.Fprintf(os.Stderr, "[debug] parsed %d services from %s\n", len(tt.Services), filepath.Base(jsonPath))
			}
			if len(tt.Services) > 0 {
				// Attach the per-route RVD map so the writer can resolve vehicle classes.
				tt.RVDByPath = rvdMap
				tt.OriginLat = originLat
				tt.OriginLng = originLng
				tt.Ribbons = ribbonMap
				tt.RouteRibbons = routeOnlyRibbons
				tt.LinkedPlatforms = linkedPlatforms
				tt.Signals = signals
				tt.Switches = switches
				tt.CarStopSigns = carStopSigns
				tt.RouteMarkers = routeMarkers
				all = append(all, tt)
			}
		}
	}

	return all, nil
}

// scanRVDs walks the extracted pak for RailVehicleDefinition assets (files
// whose basename starts with "RVD_"), converts each with UAssetGUI, and
// parses out the subset of fields we need (RailVehicleClass, length, etc.).
// Failures are logged but don't abort the route.
func (e *Extractor) scanRVDs(root, routeName string) map[string]*uasset.RVD {
	var rvdPaths []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := filepath.Base(path)
		if strings.HasPrefix(name, "RVD_") && strings.HasSuffix(name, ".uasset") {
			rvdPaths = append(rvdPaths, path)
		}
		return nil
	})
	if len(rvdPaths) == 0 {
		return nil
	}
	e.logf("[%s] Parsing %d RVD asset(s)...\n", routeName, len(rvdPaths))
	out := make(map[string]*uasset.RVD, len(rvdPaths))
	for _, ua := range rvdPaths {
		jsonPath := ua + ".json"
		if err := e.runUAssetGUI(ua, jsonPath); err != nil {
			if e.cfg.Debug {
				fmt.Fprintf(os.Stderr, "[%s] RVD convert failed on %s: %v\n", routeName, filepath.Base(ua), err)
			}
			continue
		}
		rvd, err := uasset.ParseRVD(jsonPath)
		if err != nil {
			if e.cfg.Debug {
				fmt.Fprintf(os.Stderr, "[%s] RVD parse failed on %s: %v\n", routeName, filepath.Base(ua), err)
			}
			continue
		}
		// Key by a canonical asset path fragment that matches what appears in
		// CompiledRVMap: everything from the last "Plugins/DLC/" onwards,
		// minus extension, with forward slashes.
		key := canonicalRVDPath(ua)
		out[key] = rvd
	}
	return out
}

// canonicalRVDPath turns an on-disk file path into the short asset-path form
// used inside CompiledRVMap entries (e.g.
// "/LIRREX_M7/Data/RailVehicleDefinition/RVD_LIRREX_M7-A.RVD_LIRREX_M7-A").
// We produce the path-only prefix; the exact ".Name.Name" suffix UAssetGUI
// emits is matched with a tolerant lookup on the consumer side.
func canonicalRVDPath(diskPath string) string {
	p := filepath.ToSlash(diskPath)
	const marker = "/Plugins/DLC/"
	if idx := strings.Index(p, marker); idx >= 0 {
		p = p[idx+len(marker)-1:] // keep leading "/"
	}
	// Strip /Content/ since the in-asset path uses /PluginName/... (no /Content/).
	p = strings.Replace(p, "/Content/", "/", 1)
	// Drop ".uasset" extension
	p = strings.TrimSuffix(p, ".uasset")
	return p
}

// runUnrealPak extracts a .pak file using repak (preferred) or UnrealPak.
func (e *Extractor) runUnrealPak(pakPath, destDir string) error {
	if e.cfg.RepakPath != "" {
		return e.runRepak(pakPath, destDir)
	}
	return e.runUnrealPakLegacy(pakPath, destDir)
}

// runRepak uses repak.exe which supports Oodle-compressed paks.
func (e *Extractor) runRepak(pakPath, destDir string) error {
	args := []string{}
	if e.cfg.AESKey != "" {
		args = append(args, "--aes-key", "0x"+e.cfg.AESKey)
	}
	args = append(args, "unpack", "--output", destDir, pakPath)
	cmd := exec.Command(e.cfg.RepakPath, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// runUnrealPakLegacy uses UnrealPak.exe (no Oodle support).
func (e *Extractor) runUnrealPakLegacy(pakPath, destDir string) error {
	args := []string{pakPath, "-Extract", destDir}
	if e.cfg.AESKey != "" {
		cryptoPath, err := writeCryptoJSON(e.cfg.AESKey)
		if err != nil {
			return fmt.Errorf("writing crypto key: %w", err)
		}
		defer os.Remove(cryptoPath)
		args = append(args, "-cryptokeys="+cryptoPath)
	}
	cmd := exec.Command(e.cfg.UnrealPakPath, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// writeCryptoJSON writes a temporary Crypto.json with the given AES key and
// returns its path. The caller is responsible for deleting it.
func writeCryptoJSON(aesKey string) (string, error) {
	const tmpl = `{
  "$types": {
    "UnrealBuildTool.EncryptionAndSigning+CryptoSettings, UnrealBuildTool, Version=4.0.0.0, Culture=neutral, PublicKeyToken=null": "1",
    "UnrealBuildTool.EncryptionAndSigning+EncryptionKey, UnrealBuildTool, Version=4.0.0.0, Culture=neutral, PublicKeyToken=null": "2"
  },
  "$type": "1",
  "EncryptionKey": {
    "$type": "2",
    "Name": null,
    "Guid": null,
    "Key": %q
  },
  "SigningKey": null,
  "bEnablePakSigning": false,
  "bEnablePakIndexEncryption": true,
  "bEnablePakIniEncryption": true,
  "bEnablePakUAssetEncryption": true,
  "bEnablePakFullAssetEncryption": false,
  "bDataCryptoRequired": true,
  "PakEncryptionRequired": true,
  "PakSigningRequired": false,
  "SecondaryEncryptionKeys": []
}`
	f, err := os.CreateTemp("", "tsw6-crypto-*.json")
	if err != nil {
		return "", err
	}
	defer f.Close()
	fmt.Fprintf(f, tmpl, aesKey)
	return f.Name(), nil
}

// runUAssetGUI converts a .uasset to JSON using the UAssetGUI CLI.
func (e *Extractor) runUAssetGUI(uassetPath, jsonPath string) error {
	// UAssetGUI tojson <source> <destination> VER_UE4_27
	cmd := exec.Command(e.cfg.UAssetGUIPath,
		"tojson", uassetPath, jsonPath, "VER_UE4_27")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// findTimetableAssets walks the extracted pak directory looking for .uasset
// files that contain timetable-shaped data. Matches:
//   - filename/path contains "timetable", "servicemode" or "service_mode"
//     (the original base-game timetable assets)
//   - path contains a "/scenarios/" or "/training/" directory boundary —
//     scenario/tutorial paks store their schedule assets here under names
//     that don't necessarily include "timetable" in the basename, so we let
//     the downstream parser decide whether each file is actually parseable.
func findTimetableAssets(root string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			return nil
		}
		name := filepath.Base(path)
		if !strings.HasSuffix(name, ".uasset") {
			return nil
		}
		// Check relative path only — the temp dir name contains "timetable"
		// which would otherwise match every file.
		rel, _ := filepath.Rel(root, path)
		relLower := strings.ToLower(rel)
		// Normalise separators so "/scenarios/" matches on Windows too.
		relSlash := strings.ReplaceAll(relLower, `\`, `/`)
		if strings.Contains(relLower, "timetable") ||
			strings.Contains(relLower, "servicemode") ||
			strings.Contains(relLower, "service_mode") ||
			strings.Contains(relSlash, "/scenarios/") ||
			strings.Contains(relSlash, "/training/") {
			found = append(found, path)
		}
		return nil
	})
	return found, err
}

func (e *Extractor) logf(format string, args ...any) {
	if e.cfg.Verbose {
		fmt.Fprintf(os.Stderr, format, args...)
	}
}