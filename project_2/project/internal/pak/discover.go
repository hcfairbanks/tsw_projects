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
