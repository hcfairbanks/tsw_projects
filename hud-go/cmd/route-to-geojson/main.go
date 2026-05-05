// route-to-geojson — CLI shim around internal/cookedmap. Walks an unpacked
// pak directory and emits the route-level GeoJSON FeatureCollection used
// by the viewer and (now) the web extractor's per-route zip.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"hud-go/internal/cookedmap"
)

func main() {
	workdir := flag.String("workdir", "", "directory of unpacked tile umaps (recursive)")
	out := flag.String("out", "", "output GeoJSON path")
	originLat := flag.Float64("origin-lat", 0, "route origin latitude (auto-detected if 0)")
	originLng := flag.Float64("origin-lng", 0, "route origin longitude (auto-detected if 0)")
	routeName := flag.String("name", "route", "route name in the GeoJSON properties")
	flag.Parse()
	if *workdir == "" || *out == "" {
		log.Fatal("usage: route-to-geojson --workdir <dir> --out <geojson> [--origin-lat ... --origin-lng ...] [--name <name>]")
	}

	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}
	w, err := os.Create(*out)
	if err != nil {
		log.Fatalf("create: %v", err)
	}
	defer w.Close()

	stats, err := cookedmap.Build(cookedmap.Options{
		Workdir:   *workdir,
		RouteName: *routeName,
		OriginLat: *originLat,
		OriginLng: *originLng,
		Logger:    func(format string, args ...any) { fmt.Fprintf(os.Stderr, format, args...) },
	}, w)
	if err != nil {
		log.Fatalf("build: %v", err)
	}
	fmt.Fprintf(os.Stderr, "[route-to-geojson] wrote %s in %s (%d features)\n",
		*out, stats.Elapsed.Round(1e6), stats.Features)
}
