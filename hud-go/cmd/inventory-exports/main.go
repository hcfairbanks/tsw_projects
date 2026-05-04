// inventory-exports — scan every umap tile in a route and report the
// distinct ObjectName "kinds" present, with counts per tile-type.
//
// We use ObjectName pattern (the FName before the trailing _N suffix and
// stripped of "Default__") as a proxy for class. This is good enough to
// discover what geometry/feature types live in each tile family without
// needing to walk the full property tree.
//
// Output: a sorted table of "kind: TT=count TS=count ST=count LT=count
// SST=count OTHER=count" so we can spot extraction candidates.
//
// Usage:
//   inventory-exports --route TrainingCentre --workdir <unpacked-tiles-dir>
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"hud-go/internal/pak/uasset"
)

// stripSuffix turns "NetworkRenderComponent_3" into "NetworkRenderComponent"
// so per-instance numbering doesn't fragment the inventory. Also drops a
// "Default__" prefix since those are class templates, not instances.
var trailingNum = regexp.MustCompile(`_\d+$`)

func canonicalKind(name string) string {
	name = strings.TrimPrefix(name, "Default__")
	name = trailingNum.ReplaceAllString(name, "")
	return name
}

// tileFamily maps a filename to the tile-type prefix (TT, TS, ST, LT, SST,
// or OTHER). The tile prefix tells us where to look for which feature class.
func tileFamily(name string) string {
	for _, p := range []string{"SST_", "TT_", "TS_", "ST_", "LT_"} {
		if strings.HasPrefix(name, p) {
			return strings.TrimSuffix(p, "_")
		}
	}
	return "OTHER"
}

func main() {
	workdir := flag.String("workdir", "", "dir of unpacked .umap tiles (recursive)")
	minTotal := flag.Int("min", 1, "hide kinds with fewer total instances")
	flag.Parse()
	if *workdir == "" {
		log.Fatal("missing --workdir")
	}

	families := []string{"TT", "TS", "ST", "LT", "SST", "OTHER"}
	// kind -> family -> count
	counts := map[string]map[string]int{}
	tileCount := 0
	skipped := 0

	err := filepath.WalkDir(*workdir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".umap") {
			return nil
		}
		fam := tileFamily(d.Name())
		u, perr := uasset.ReadUmap(p)
		if perr != nil {
			skipped++
			return nil
		}
		tileCount++
		for _, e := range u.Exports {
			k := canonicalKind(e.ObjectName)
			if k == "" {
				continue
			}
			if counts[k] == nil {
				counts[k] = map[string]int{}
			}
			counts[k][fam]++
		}
		return nil
	})
	if err != nil {
		log.Fatalf("walk: %v", err)
	}

	fmt.Fprintf(os.Stderr, "tiles read: %d  (skipped: %d)\n", tileCount, skipped)

	// Sort by total count desc.
	type row struct {
		kind  string
		total int
		cells []int // one per family
	}
	rows := make([]row, 0, len(counts))
	for k, fc := range counts {
		var r row
		r.kind = k
		r.cells = make([]int, len(families))
		for i, f := range families {
			r.cells[i] = fc[f]
			r.total += fc[f]
		}
		if r.total >= *minTotal {
			rows = append(rows, r)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].total > rows[j].total })

	// Header
	fmt.Printf("%-50s %8s", "Kind", "TOTAL")
	for _, f := range families {
		fmt.Printf(" %8s", f)
	}
	fmt.Println()
	fmt.Println(strings.Repeat("-", 50+9*(len(families)+1)))
	for _, r := range rows {
		fmt.Printf("%-50s %8d", r.kind, r.total)
		for _, c := range r.cells {
			if c == 0 {
				fmt.Printf(" %8s", ".")
			} else {
				fmt.Printf(" %8d", c)
			}
		}
		fmt.Println()
	}
}
