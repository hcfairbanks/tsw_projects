// probe-signals — diagnostic. Prints (a) what ParseCookedFeaturesFromUmap
// extracts and (b) the property tree of the first UK_SignalModel_C / Signal
// export via the engine-level DumpExport. Comparing the two reveals whether
// the per-field walker is missing properties the binary parser CAN see.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"hud-go/internal/pak/uasset"
)

func main() {
	in := flag.String("umap", "", "path to a .umap file")
	flag.Parse()
	if *in == "" {
		log.Fatal("missing --umap")
	}
	u, err := uasset.ReadUmap(*in)
	if err != nil {
		log.Fatalf("readumap: %v", err)
	}

	fts, err := uasset.ParseCookedFeaturesFromUmap(*in, "diag")
	if err != nil {
		log.Fatalf("parse: %v", err)
	}
	fmt.Printf("ParseCookedFeaturesFromUmap: signals=%d switches=%d markers=%d carstops=%d platforms=%d\n",
		len(fts.Signals), len(fts.Switches), len(fts.RouteMarkers), len(fts.CarStopSigns), len(fts.Platforms))
	count := 0
	fmt.Println("\n--- ALL exports whose name contains 'Signal' ---")
	for _, e := range u.Exports {
		if !strings.Contains(e.ObjectName, "Signal") {
			continue
		}
		fmt.Printf("  %q  size=%d  outerIdx=%d\n", e.ObjectName, e.SerialSize, e.OuterIndex)
		count++
	}
	fmt.Printf("(%d total)\n", count)

	fmt.Println("\n--- exports whose ObjectName contains 'Signal' ---")
	dumped := 0
	for _, e := range u.Exports {
		if !strings.Contains(e.ObjectName, "Signal") {
			continue
		}
		if strings.HasPrefix(e.ObjectName, "Default__") {
			continue
		}
		fmt.Printf("\n[export] %s (size=%d)\n", e.ObjectName, e.SerialSize)
		u.DumpExport(os.Stdout, e, 0)
		dumped++
		if dumped >= 1 {
			break
		}
	}
	if dumped == 0 {
		fmt.Println("(no Signal-named exports in this tile)")
	}
}
