// find-origin — scan an unpacked TSW route for the persistent-map .uexp and
// print its CentralLatitude/CentralLongitude.
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
	"strings"

	"hud-go/internal/geo"
)

func main() {
	root := flag.String("workdir", "", "directory of unpacked route content (recursive)")
	flag.Parse()
	if *root == "" {
		log.Fatal("missing --workdir")
	}
	var found bool
	_ = filepath.WalkDir(*root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(p), ".uexp") {
			return nil
		}
		// Match the convention tc-hermite uses: parent directory must be "Map"
		parts := strings.Split(filepath.ToSlash(p), "/")
		if len(parts) < 2 || !strings.EqualFold(parts[len(parts)-2], "Map") {
			return nil
		}
		la, ln, err := geo.ExtractOriginFromUExp(p)
		if err == nil && la != 0 && ln != 0 {
			fmt.Printf("file: %s\nlat:  %.14f\nlng:  %.14f\n", p, la, ln)
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	if !found {
		log.Fatal("no origin found")
	}
}
