// extract-ribbon-groups walks every TS_*.umap tile of a TSW6 route and
// extracts NetworkMultiRibbonRange ribbon-groupings using our in-process
// Go uasset parser. No UAssetGUI involvement, so no OOM on big tiles.
//
// Output JSON has the same shape as the original Python extractor
// (extract_ribbon_groups.py), so tc-hermite's --ribbon-groups flag consumes
// either source unchanged.
//
//   {
//     "route":  "<RouteName>",
//     "groups": [["guid1", "guid2", ...], ...],
//     "trust":  {"<guid>": ["<co-grouped-guid>", ...], ...}
//   }
//
// Usage:
//
//   extract-ribbon-groups.exe --route TrainingCentre \
//     --workdir <dir-with-extracted-tiles>  \
//     --out C:\Users\hcfai\Desktop\tc_ribbon_groups.json
//
// If --workdir is empty, we unpack the route's pak via repak first
// (matching tc-hermite's flow).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"hud-go/internal/devtools"
	"hud-go/internal/pak"
	"hud-go/internal/pak/uasset"
)

const (
	tswPath = `D:\SteamLibrary\steamapps\common\Train Sim World 6`
)

var repakBin = devtools.MustFindBin("repak")

func main() {
	routeFlag := flag.String("route", "", "route codename (matches DLC pak filename)")
	workdir := flag.String("workdir", "", "dir with already-unpacked tiles; if empty, we unpack the pak first")
	outPath := flag.String("out", "", "output JSON path (default: <Desktop>/<Route>_ribbon_groups_go.json)")
	verbose := flag.Bool("v", false, "print per-tile group counts")
	flag.Parse()

	if *routeFlag == "" {
		log.Fatal("missing --route")
	}
	if *outPath == "" {
		*outPath = filepath.Join(
			os.Getenv("USERPROFILE"), "Desktop",
			fmt.Sprintf("%s_ribbon_groups_go.json", *routeFlag),
		)
	}

	tsTiles, err := locateTSTiles(*routeFlag, *workdir)
	if err != nil {
		log.Fatalf("locate tiles: %v", err)
	}
	fmt.Fprintf(os.Stderr, "[extract-ribbon-groups] route=%s ts-tiles=%d\n",
		*routeFlag, len(tsTiles))

	t0 := time.Now()
	var allGroups [][]string
	skipped := 0
	withData := 0
	for i, tile := range tsTiles {
		groups, err := uasset.ParseRibbonGroupsFromUmap(tile)
		if err != nil {
			skipped++
			fmt.Fprintf(os.Stderr, "  skip %s: %v\n", filepath.Base(tile), err)
			continue
		}
		if len(groups) > 0 {
			withData++
		}
		for _, g := range groups {
			allGroups = append(allGroups, dedupeAndSort(g.GUIDs))
		}
		if *verbose && len(groups) > 0 {
			fmt.Fprintf(os.Stderr, "  %s: %d group(s)\n", filepath.Base(tile), len(groups))
		}
		if (i+1)%50 == 0 || i+1 == len(tsTiles) {
			fmt.Fprintf(os.Stderr, "  %d/%d tiles, %d groups so far (%.0fs)\n",
				i+1, len(tsTiles), len(allGroups), time.Since(t0).Seconds())
		}
	}
	fmt.Fprintf(os.Stderr, "\nGroups extracted:    %d\n", len(allGroups))
	fmt.Fprintf(os.Stderr, "Tiles with data:     %d\n", withData)
	fmt.Fprintf(os.Stderr, "Tiles skipped/error: %d\n", skipped)

	// Build trust graph: each ribbon -> set of co-grouped ribbons.
	trust := map[string]map[string]bool{}
	for _, grp := range allGroups {
		for _, g := range grp {
			if trust[g] == nil {
				trust[g] = map[string]bool{}
			}
			for _, h := range grp {
				if h != g {
					trust[g][h] = true
				}
			}
		}
	}
	trustOut := map[string][]string{}
	for g, set := range trust {
		ks := make([]string, 0, len(set))
		for k := range set {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		trustOut[g] = ks
	}
	fmt.Fprintf(os.Stderr, "Distinct ribbons in trust graph: %d\n", len(trustOut))

	out := struct {
		Route  string              `json:"route"`
		Groups [][]string          `json:"groups"`
		Trust  map[string][]string `json:"trust"`
	}{*routeFlag, allGroups, trustOut}

	data, err := json.Marshal(out)
	if err != nil {
		log.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(*outPath, data, 0o644); err != nil {
		log.Fatalf("write %s: %v", *outPath, err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", *outPath)
}

func dedupeAndSort(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// locateTSTiles either unpacks the route's pak via repak or scans an existing
// workdir for TS_*.umap files. We deliberately mirror what tc-hermite +
// extract_ribbon_groups.py do so the same workdirs are reusable.
func locateTSTiles(route, workdir string) ([]string, error) {
	if workdir != "" {
		var tiles []string
		err := filepath.WalkDir(workdir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if !d.IsDir() && strings.HasSuffix(d.Name(), ".umap") &&
				strings.HasPrefix(d.Name(), "TS_") {
				tiles = append(tiles, p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		sort.Strings(tiles)
		return tiles, nil
	}

	// No workdir — unpack from the pak.
	routes, err := pak.DiscoverRoutes(tswPath)
	if err != nil {
		return nil, err
	}
	var rt *pak.Route
	for i := range routes {
		if strings.EqualFold(routes[i].Name, route) {
			rt = &routes[i]
			break
		}
	}
	if rt == nil {
		return nil, fmt.Errorf("route %q not found", route)
	}

	tmp := filepath.Join(os.Getenv("USERPROFILE"), "Desktop",
		fmt.Sprintf("ribgrp-%s-%d", route, time.Now().Unix()))
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return nil, err
	}
	prefix := strings.TrimPrefix(filepath.Base(rt.PakPath), "TS2Prototype-WindowsNoEditor-")
	prefix = strings.TrimSuffix(prefix, "-coredata.pak")
	prefix = strings.TrimSuffix(prefix, ".pak")
	include := "TS2Prototype/Plugins/DLC/" + prefix + "/Content/Map/Tiles"
	cmd := exec.Command(repakBin, "unpack", "-f", "-o", tmp, "-i", include, rt.PakPath)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("repak unpack: %w", err)
	}
	return locateTSTiles(route, tmp)
}
