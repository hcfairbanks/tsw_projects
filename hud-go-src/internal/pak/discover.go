package pak

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Route represents one TSW6 DLC/route pak file.
type Route struct {
	Name    string
	PakPath string
}

// DiscoverRoutes scans the TSW6 install directory for route pak files.
//
// TSW6 Steam layout (flat — all paks directly in DLC/):
//
//	<tswRoot>\WindowsNoEditor\TS2Prototype\Content\DLC\TS2Prototype-WindowsNoEditor-<RouteName>.pak
//
// Older/alternative layout (nested — each route in its own subdirectory):
//
//	<tswRoot>\WindowsNoEditor\TS2Prototype\Content\DLC\<RouteName>\<RouteName>-WindowsNoEditor.pak
//
// Plus the bundled-with-the-game routes that live next to the main pak
// rather than under DLC/:
//
//	<tswRoot>\WindowsNoEditor\TS2Prototype\Content\Paks\TS2Prototype-WindowsNoEditor-<RouteName>-coredata.pak
//
// (Training Centre is the canonical example.) The base-game pak
// `TS2Prototype-WindowsNoEditor.pak` is intentionally excluded — it has
// no RouteDefinition and isn't a route by itself.
func DiscoverRoutes(tswRoot string) ([]Route, error) {
	dlcRoot := filepath.Join(tswRoot, "WindowsNoEditor", "TS2Prototype", "Content", "DLC")

	entries, err := os.ReadDir(dlcRoot)
	if err != nil {
		return nil, fmt.Errorf("could not find DLC directory under %s: %w", tswRoot, err)
	}

	var routes []Route

	for _, e := range entries {
		if e.IsDir() {
			// Nested layout: subdirectory contains pak files
			routeDir := filepath.Join(dlcRoot, e.Name())
			paks, _ := filepath.Glob(filepath.Join(routeDir, "*.pak"))
			for _, p := range paks {
				routes = append(routes, Route{
					Name:    e.Name(),
					PakPath: p,
				})
			}
		} else if strings.HasSuffix(e.Name(), ".pak") {
			// Flat layout: TS2Prototype-WindowsNoEditor-<RouteName>.pak
			name := routeNameFromPak(e.Name())
			if name == "" {
				continue
			}
			routes = append(routes, Route{
				Name:    name,
				PakPath: filepath.Join(dlcRoot, e.Name()),
			})
		}
	}

	// Also pull in the coredata paks alongside the main game pak.
	// TS2Prototype-WindowsNoEditor-<RouteName>-coredata.pak is the
	// pattern; the main game pak (TS2Prototype-WindowsNoEditor.pak with
	// no -<RouteName>-coredata suffix) is filtered out by routeNameFromCoredata.
	paksRoot := filepath.Join(tswRoot, "WindowsNoEditor", "TS2Prototype", "Content", "Paks")
	if pakEntries, err := os.ReadDir(paksRoot); err == nil {
		for _, e := range pakEntries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".pak") {
				continue
			}
			name := routeNameFromCoredata(e.Name())
			if name == "" {
				continue
			}
			routes = append(routes, Route{
				Name:    name,
				PakPath: filepath.Join(paksRoot, e.Name()),
			})
		}
	}

	return routes, nil
}

// routeNameFromPak extracts a human-readable route name from a flat pak filename.
// e.g. "TS2Prototype-WindowsNoEditor-IsleOfWight.pak" → "IsleOfWight"
// Returns "" for filenames that don't match the expected pattern.
func routeNameFromPak(filename string) string {
	name := strings.TrimSuffix(filename, ".pak")
	// Expected prefix for route paks
	const prefix = "TS2Prototype-WindowsNoEditor-"
	if !strings.HasPrefix(name, prefix) {
		return ""
	}
	return strings.TrimPrefix(name, prefix)
}

// routeNameFromCoredata extracts the route codename from a coredata pak
// filename. e.g. "TS2Prototype-WindowsNoEditor-TrainingCentre-coredata.pak"
// → "TrainingCentre". Returns "" for the base-game pak
// (`TS2Prototype-WindowsNoEditor.pak`) and anything that doesn't end in
// `-coredata.pak`.
func routeNameFromCoredata(filename string) string {
	name := strings.TrimSuffix(filename, ".pak")
	const prefix = "TS2Prototype-WindowsNoEditor-"
	const suffix = "-coredata"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
}
