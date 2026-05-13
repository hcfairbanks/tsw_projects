// extract-texture — decode a cooked Texture2D uasset to PNG.
//
// Usage:
//   extract-texture --in <texture.uasset> --out <output.png>
//
// Currently supports PF_DXT1 only (the format every TSW6 route map texture
// uses). For other formats the call returns an error rather than producing a
// garbled image.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"hud-go/internal/pak/uasset"
)

func main() {
	in := flag.String("in", "", "input .uasset path (the .uexp sibling is read automatically)")
	out := flag.String("out", "", "output .png path")
	flag.Parse()
	if *in == "" || *out == "" {
		log.Fatal("usage: extract-texture --in <texture.uasset> --out <out.png>")
	}
	info, err := uasset.ExtractTexture2DPNG(*in, *out)
	if err != nil {
		log.Fatalf("extract: %v", err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s  (%dx%d  %s  %d mip(s))\n",
		*out, info.SizeX, info.SizeY, info.PixelFormat, info.NumMips)
}
