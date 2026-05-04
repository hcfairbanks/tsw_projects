// dump-export — print the property tree of the first export matching a
// substring in a .umap file. Uses the package's DumpExport.
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
	umap := flag.String("umap", "", "path to a .umap file")
	match := flag.String("match", "SplineMesh", "ObjectName substring to match (case-sensitive)")
	count := flag.Int("count", 1, "dump up to N matching exports")
	arrayMax := flag.Int("array-max", 5, "max array elements to print per array property")
	flag.Parse()

	if *umap == "" {
		log.Fatal("missing --umap")
	}
	u, err := uasset.ReadUmap(*umap)
	if err != nil {
		log.Fatalf("read: %v", err)
	}

	dumped := 0
	for _, e := range u.Exports {
		if !strings.Contains(e.ObjectName, *match) {
			continue
		}
		if strings.HasPrefix(e.ObjectName, "Default__") {
			continue
		}
		u.DumpExport(os.Stdout, e, *arrayMax)
		fmt.Println()
		dumped++
		if dumped >= *count {
			break
		}
	}
	if dumped == 0 {
		fmt.Fprintf(os.Stderr, "no exports matched %q\n", *match)
	} else {
		fmt.Fprintf(os.Stderr, "dumped %d export(s)\n", dumped)
	}
}
