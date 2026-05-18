// _datatrack — extract one service's DataTrack from a TSW6 pak and print
// its track-data breadcrumb (ordered (ribbon, fraction) waypoints) as
// either a human-readable table or JSON. Diagnostic / research only.
//
// Why: the master timetable carries stops and instructions but does NOT
// carry the exact path between them. Where parallel ribbons run close
// (yard throats, station approaches) the path our extractor draws can land
// on the wrong physical track. The game's RouteTimetableDataTrack asset
// holds a pre-baked, per-service ordered list of (ribbon, ribbon-fraction)
// breadcrumbs — TrackData — that traces the EXACT in-game path.
//
// This probe pulls one service's TrackData out so we can visually confirm
// the parser before plumbing it through the extractor + importer + path
// builder.
//
// Example invocation (Boston Sprinter, MBTA Franklin #741 Outbound):
//
//	go run ./cmd/_datatrack \
//	    --pak "D:\SteamLibrary\steamapps\common\Train Sim World 6\WindowsNoEditor\TS2Prototype\Content\DLC\TS2Prototype-WindowsNoEditor-BostonProvidenceGameplayPack.pak" \
//	    --datatrack "TS2Prototype/Plugins/DLC/BPE_MBTA_HSP46_GameplayPack/Content/Timetable/DataTracks/BPE_HSP46_Timetable_Final_MasterDataTrack" \
//	    --service "MBTA Franklin #741 (Outbound)"
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"hud-go/internal/devtools"
	"hud-go/internal/pak/uasset"
)

func main() {
	pakPath := flag.String("pak", "", "path to the .pak containing the DataTrack uasset")
	dtPath := flag.String("datatrack", "", "in-pak path to the DataTrack uasset (no .uasset/.uexp suffix)")
	service := flag.String("service", "", "Service name to dump (exact). If empty, list all service keys + entry counts.")
	asJSON := flag.Bool("json", false, "emit JSON instead of a table")
	limit := flag.Int("limit", 0, "limit number of TrackData rows printed (0 = all)")
	outFile := flag.String("out", "", "write to file instead of stdout")
	flag.Parse()
	if *pakPath == "" || *dtPath == "" {
		log.Fatal("missing --pak and/or --datatrack")
	}

	uassetPath, cleanup, err := extractPair(*pakPath, *dtPath)
	if err != nil {
		log.Fatalf("extract: %v", err)
	}
	defer cleanup()

	dt, err := uasset.ParseCookedDataTrack(uassetPath)
	if err != nil {
		log.Fatalf("parse: %v", err)
	}

	var w io.Writer = os.Stdout
	if *outFile != "" {
		f, err := os.Create(*outFile)
		if err != nil {
			log.Fatalf("create: %v", err)
		}
		defer f.Close()
		w = f
	}

	if *service == "" {
		// Index mode: just list all keys with row counts.
		keys := make([]string, 0, len(dt.Services))
		for k := range dt.Services {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(os.Stderr, "DataTrack %s: VersionNumber=%d, %d services\n",
			dt.Source, dt.VersionNumber, len(dt.Services))
		if *asJSON {
			summary := make([]map[string]any, 0, len(keys))
			for _, k := range keys {
				summary = append(summary, map[string]any{
					"service":         k,
					"track_data_rows": len(dt.Services[k].TrackData),
				})
			}
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			enc.Encode(summary)
		} else {
			for _, k := range keys {
				fmt.Fprintf(w, "  %-60s  rows=%d\n", k, len(dt.Services[k].TrackData))
			}
		}
		return
	}

	std, ok := dt.Services[*service]
	if !ok {
		fmt.Fprintf(os.Stderr, "service %q not found. Available services (run without --service to list):\n", *service)
		// Best-effort: print 5 candidates that share a prefix.
		for k := range dt.Services {
			if strings.HasPrefix(k, firstWords(*service, 2)) {
				fmt.Fprintf(os.Stderr, "  %s\n", k)
			}
		}
		os.Exit(1)
	}

	if *asJSON {
		out := std
		if *limit > 0 && *limit < len(out.TrackData) {
			out.TrackData = out.TrackData[:*limit]
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.Encode(out)
		return
	}

	fmt.Fprintf(w, "Service: %s\n", std.ServiceName)
	fmt.Fprintf(w, "  TrackData rows: %d\n", len(std.TrackData))
	fmt.Fprintf(w, "  ActionIndices : %v\n\n", std.ActionIndices)
	fmt.Fprintf(w, "  %-5s  %-32s  %-8s  %-12s  %-18s  %-10s  %s\n",
		"#", "ribbon_guid", "frac", "distance_cm", "data_type", "direction", "instr_idx/govia_idx")
	rows := std.TrackData
	if *limit > 0 && *limit < len(rows) {
		rows = rows[:*limit]
	}
	for i, r := range rows {
		fmt.Fprintf(w, "  %-5d  %-32s  %-8.4f  %-12.1f  %-18s  %-10s  %d/%d\n",
			i, r.RibbonGUID, r.RibbonLocation, r.Distance, r.DataType, r.Direction,
			r.InstructionIndex, r.GoViaIndex)
	}
}

// extractPair pulls the .uasset and .uexp out of `pak` to a temp dir and
// returns the .uasset path plus a cleanup func.
func extractPair(pakPath, inPakStem string) (string, func(), error) {
	bin, err := devtools.FindBin("repak")
	if err != nil {
		return "", nil, fmt.Errorf("repak: %w", err)
	}
	tmp, err := os.MkdirTemp("", "datatrack-probe-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(tmp) }
	// repak's `get` reads to stdout — pipe to a file ourselves.
	for _, ext := range []string{".uasset", ".uexp"} {
		inFile := inPakStem + ext
		outPath := filepath.Join(tmp, filepath.Base(inPakStem)+ext)
		if err := runRepakGet(bin, pakPath, inFile, outPath); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("repak get %s: %w", inFile, err)
		}
	}
	return filepath.Join(tmp, filepath.Base(inPakStem)+".uasset"), cleanup, nil
}

func runRepakGet(bin, pakPath, inFile, outPath string) error {
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd := exec.Command(bin, "get", pakPath, inFile)
	cmd.Stdout = f
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func firstWords(s string, n int) string {
	parts := strings.Fields(s)
	if len(parts) > n {
		parts = parts[:n]
	}
	return strings.Join(parts, " ")
}
