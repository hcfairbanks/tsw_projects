// peek-namemap — print every name in a single uasset's NameMap, plus per-export offset/size.
// Used to discover where in the .uexp body to seek for non-property data
// (e.g. Texture2D's FTexturePlatformData).
package main

import (
	"flag"
	"fmt"
	"log"

	"hud-go/internal/pak/uasset"
)

func main() {
	in := flag.String("in", "", "uasset path")
	flag.Parse()
	if *in == "" {
		log.Fatal("missing --in")
	}
	u, err := uasset.ReadUmap(*in)
	if err != nil {
		log.Fatalf("read: %v", err)
	}
	fmt.Println("=== NameMap ===")
	for i, n := range u.Names {
		fmt.Printf("  [%d] %s\n", i, n)
	}
	fmt.Println("=== Exports ===")
	for i, e := range u.Exports {
		fmt.Printf("  [%d] name=%s class=%d outer=%d serialOffset=%d size=%d\n",
			i, e.ObjectName, e.ClassIndex, e.OuterIndex, e.SerialOffset, e.SerialSize)
	}
}
