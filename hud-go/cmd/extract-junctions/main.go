// extract-junctions — walk every TT_*.umap tile in a route and emit a
// JSON list of NetworkTurnoutJunction connections. Consumed by tc-hermite's
// --junctions flag to constrain walker decisions at switch nodes.
//
// Output JSON shape:
//
//	{
//	  "route": "TrainingCentre",
//	  "junctions": [
//	    {
//	      "node": "<node_guid>",
//	      "ingoing":  "<ribbon_guid>",
//	      "ingoing_start":  false,
//	      "outgoing": "<ribbon_guid>",
//	      "outgoing_start": true,
//	      "turnout":  "<ribbon_guid>",
//	      "turnout_start":  true
//	    },
//	    ...
//	  ]
//	}
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

type junctionJSON struct {
	Node          string `json:"node"`
	Ingoing       string `json:"ingoing"`
	IngoingStart  bool   `json:"ingoing_start"`
	Outgoing      string `json:"outgoing"`
	OutgoingStart bool   `json:"outgoing_start"`
	Turnout       string `json:"turnout"`
	TurnoutStart  bool   `json:"turnout_start"`
}

func main() {
	routeFlag := flag.String("route", "", "route codename")
	workdir := flag.String("workdir", "", "dir with already-extracted .umap tiles. If empty, unpack via repak.")
	outPath := flag.String("out", "", "output JSON path (default: <Desktop>/<Route>_junctions.json)")
	flag.Parse()
	if *routeFlag == "" {
		log.Fatal("missing --route")
	}
	if *outPath == "" {
		*outPath = filepath.Join(os.Getenv("USERPROFILE"), "Desktop",
			fmt.Sprintf("%s_junctions.json", *routeFlag))
	}

	tiles, err := locateTTTiles(*routeFlag, *workdir)
	if err != nil {
		log.Fatalf("locate: %v", err)
	}
	fmt.Fprintf(os.Stderr, "[extract-junctions] route=%s TT-tiles=%d\n",
		*routeFlag, len(tiles))

	t0 := time.Now()
	var junctions []junctionJSON
	for i, tile := range tiles {
		js, err := uasset.ParseTurnoutJunctionsFromUmap(tile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  skip %s: %v\n", filepath.Base(tile), err)
			continue
		}
		for _, j := range js {
			junctions = append(junctions, junctionJSON{
				Node:          j.NodeGUID,
				Ingoing:       j.Ingoing,
				IngoingStart:  j.IngoingStart,
				Outgoing:      j.Outgoing,
				OutgoingStart: j.OutgoingStart,
				Turnout:       j.Turnout,
				TurnoutStart:  j.TurnoutStart,
			})
		}
		if (i+1)%50 == 0 || i+1 == len(tiles) {
			fmt.Fprintf(os.Stderr, "  %d/%d tiles, %d junctions so far (%.0fs)\n",
				i+1, len(tiles), len(junctions), time.Since(t0).Seconds())
		}
	}
	fmt.Fprintf(os.Stderr, "\nJunctions extracted: %d\n", len(junctions))

	out := struct {
		Route     string         `json:"route"`
		Junctions []junctionJSON `json:"junctions"`
	}{*routeFlag, junctions}
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
		fmt.Sprintf("junctions-%s-%d", route, time.Now().Unix()))
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
