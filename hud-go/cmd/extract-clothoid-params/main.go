// extract-clothoid-params — walk every TT_*.umap tile in a route and emit a
// JSON map of ribbon-GUID → clothoid params (A, ClothoidLength, RadialOffset,
// StartPos2D, StartTangent2D). tc-hermite consumes this to render clothoid
// transition curves via Fresnel integration instead of cubic-Hermite-with-
// inferred-end-tangent.
//
// Output JSON shape:
//
//	{
//	  "route": "TrainingCentre",
//	  "params": {
//	     "<ribbon_guid_lowercase_hex>": {
//	        "a": -187268041.07,
//	        "l": 5314.367676,
//	        "radial_offset": 0.0,
//	        "start_x": 18486.39,
//	        "start_y": 46777.42,
//	        "start_tan_x": -0.61,
//	        "start_tan_y": -0.79
//	     },
//	     ...
//	  }
//	}
//
// Usage:
//
//	extract-clothoid-params.exe --route TrainingCentre \
//	    --workdir <dir-with-extracted-TT-tiles> \
//	    --out C:\Users\hcfai\Desktop\TC_clothoids.json
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

	"hud-go/internal/pak"
	"hud-go/internal/pak/uasset"
)

const (
	tswPath  = `D:\SteamLibrary\steamapps\common\Train Sim World 6`
	repakBin = `C:\Users\hcfai\Desktop\applications_2\hud-go\repak.exe`
)

type clothoidJSON struct {
	A            float64 `json:"a"`
	L            float64 `json:"l"`
	RadialOffset float64 `json:"radial_offset"`
	StartX       float64 `json:"start_x"`
	StartY       float64 `json:"start_y"`
	StartTanX    float64 `json:"start_tan_x"`
	StartTanY    float64 `json:"start_tan_y"`
}

func main() {
	routeFlag := flag.String("route", "", "route codename")
	workdir := flag.String("workdir", "", "dir with already-extracted .umap tiles. If empty, unpack via repak.")
	outPath := flag.String("out", "", "output JSON path (default: <Desktop>/<Route>_clothoid_params.json)")
	flag.Parse()
	if *routeFlag == "" {
		log.Fatal("missing --route")
	}
	if *outPath == "" {
		*outPath = filepath.Join(os.Getenv("USERPROFILE"), "Desktop",
			fmt.Sprintf("%s_clothoid_params.json", *routeFlag))
	}

	tiles, err := locateTTTiles(*routeFlag, *workdir)
	if err != nil {
		log.Fatalf("locate: %v", err)
	}
	fmt.Fprintf(os.Stderr, "[extract-clothoid-params] route=%s TT-tiles=%d\n",
		*routeFlag, len(tiles))

	params := map[string]clothoidJSON{}
	t0 := time.Now()
	for i, tile := range tiles {
		m, err := uasset.ParseClothoidsByRibbonGUID(tile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  skip %s: %v\n", filepath.Base(tile), err)
			continue
		}
		for guid, p := range m {
			params[guid] = clothoidJSON{
				A:            p.A,
				L:            p.L,
				RadialOffset: p.RadialOffset,
				StartX:       p.StartX,
				StartY:       p.StartY,
				StartTanX:    p.StartTanX,
				StartTanY:    p.StartTanY,
			}
		}
		if (i+1)%50 == 0 || i+1 == len(tiles) {
			fmt.Fprintf(os.Stderr, "  %d/%d tiles, %d clothoids so far (%.0fs)\n",
				i+1, len(tiles), len(params), time.Since(t0).Seconds())
		}
	}
	fmt.Fprintf(os.Stderr, "\nClothoids extracted: %d\n", len(params))

	out := struct {
		Route  string                  `json:"route"`
		Params map[string]clothoidJSON `json:"params"`
	}{*routeFlag, params}
	data, err := json.Marshal(out)
	if err != nil {
		log.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(*outPath, data, 0o644); err != nil {
		log.Fatalf("write: %v", err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d bytes)\n", *outPath, len(data))
}

func locateTTTiles(route, workdir string) ([]string, error) {
	if workdir != "" {
		var tiles []string
		err := filepath.WalkDir(workdir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(d.Name(), ".umap") {
				return nil
			}
			if !strings.HasPrefix(d.Name(), "TT_") {
				return nil
			}
			tiles = append(tiles, p)
			return nil
		})
		if err != nil {
			return nil, err
		}
		sort.Strings(tiles)
		return tiles, nil
	}
	// Unpack from pak
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
		fmt.Sprintf("clothoid-%s-%d", route, time.Now().Unix()))
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
		return nil, fmt.Errorf("repak: %w", err)
	}
	return locateTTTiles(route, tmp)
}
