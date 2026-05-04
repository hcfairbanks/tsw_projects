// extract-ribbons-cooked — produce the ribbons-canonical CSV directly from
// a route's cooked .pak (or an unpacked workdir of its tiles), no editor
// required.
//
// Pipeline:
//   1. (optional) repak unpack the route's pak to a temp workdir
//   2. Walk every TT_*.umap, parse NetworkRibbon + linked curve via the
//      binary parser
//   3. Emit one CSV row per ribbon in the format ribbons-to-geojson expects
//
// CachedStartPosition (a FarVector on each NetworkRibbon) gives the world cm
// position of the ribbon's start — equivalent to the editor's
// `WorldLocation + StartPosition2D`. We store it as sx_cm/sy_cm and set
// world_loc_x/y to 0, so ribbons-to-geojson doesn't need to add anything.
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"hud-go/internal/pak/uasset"
)

const (
	repakBin = `C:\Users\hcfai\Desktop\applications_2\hud-go\repak.exe`
)

func main() {
	pakPath := flag.String("pak", "", "path to the route's cooked .pak (will be unpacked to a temp dir)")
	workdir := flag.String("workdir", "", "directory of already-unpacked tiles (recursive). If set, --pak is ignored.")
	out := flag.String("out", "", "output CSV path")
	flag.Parse()
	if *out == "" {
		log.Fatal("missing --out")
	}
	if *pakPath == "" && *workdir == "" {
		log.Fatal("need either --pak or --workdir")
	}

	root := *workdir
	cleanup := false
	if root == "" {
		tmp, err := os.MkdirTemp("", "extract-ribbons-cooked-*")
		if err != nil {
			log.Fatalf("mkdtemp: %v", err)
		}
		root = tmp
		cleanup = true
		t0 := time.Now()
		fmt.Fprintf(os.Stderr, "[extract-ribbons-cooked] unpacking %s ...\n", *pakPath)
		cmd := exec.Command(repakBin, "unpack", "--output", root, *pakPath)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Fatalf("repak: %v", err)
		}
		fmt.Fprintf(os.Stderr, "[extract-ribbons-cooked] unpacked in %s\n", time.Since(t0).Round(time.Millisecond))
	}
	if cleanup {
		defer os.RemoveAll(root)
	}

	// Find every TT_*.umap.
	var tiles []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		base := d.Name()
		if !strings.HasSuffix(strings.ToLower(base), ".umap") {
			return nil
		}
		if !strings.HasPrefix(base, "TT_") {
			return nil
		}
		tiles = append(tiles, p)
		return nil
	})
	fmt.Fprintf(os.Stderr, "[extract-ribbons-cooked] %d TT tiles\n", len(tiles))

	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(*out)
	if err != nil {
		log.Fatalf("create csv: %v", err)
	}
	defer f.Close()
	fmt.Fprintln(f, "actor_path,ribbon_name,ribbon_guid,start_node_guid,end_node_guid,curve_class,sx_cm,sy_cm,tx,ty,length_cm,radius_cm,world_loc_x,world_loc_y")

	t0 := time.Now()
	totalRibbons, totalArcs, totalClothoids, totalOther := 0, 0, 0, 0
	for i, tilePath := range tiles {
		tileName := strings.TrimSuffix(filepath.Base(tilePath), ".umap")
		ribs, perr := uasset.ParseCookedRibbonsFromUmap(tilePath, tileName)
		if perr != nil {
			fmt.Fprintf(os.Stderr, "  WARN parse %s: %v\n", tileName, perr)
			continue
		}
		// Synthesize an actor_path that ribbons-to-geojson's tile regex matches.
		actorPath := fmt.Sprintf("/Cooked/Map/Tiles/%s.%s:PersistentLevel.TrackNetworkActor",
			tileName, tileName)
		for _, r := range ribs {
			rad := ""
			if r.HasRadius {
				rad = fmt.Sprintf("%.3f", r.Radius)
			}
			class := r.CurveClass
			if class == "" {
				class = "NoCurve"
				totalOther++
			} else if class == "NetworkCurveCircularArc" {
				totalArcs++
			} else if class == "NetworkCurveClothoidSpiral" {
				totalClothoids++
			} else {
				totalOther++
			}
			fmt.Fprintf(f, "%s,%s,%s,%s,%s,%s,%.3f,%.3f,%.6f,%.6f,%.3f,%s,0,0\n",
				actorPath, r.RibbonName, r.RibbonGUID, r.StartNodeGUID, r.EndNodeGUID,
				class, r.StartX, r.StartY, r.TangentX, r.TangentY, r.Length, rad)
			totalRibbons++
		}
		if (i+1)%50 == 0 || i+1 == len(tiles) {
			fmt.Fprintf(os.Stderr, "  %d/%d tiles, %d ribbons (%s)\n",
				i+1, len(tiles), totalRibbons, time.Since(t0).Round(time.Second))
		}
	}
	fmt.Fprintf(os.Stderr, "[extract-ribbons-cooked] total: %d ribbons (%d arcs, %d clothoids, %d other) → %s\n",
		totalRibbons, totalArcs, totalClothoids, totalOther, *out)
}
