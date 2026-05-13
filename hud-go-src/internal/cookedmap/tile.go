package cookedmap

import (
	"regexp"
	"strconv"
)

// Tile filenames are `<prefix>_x<int>_y<int>` (TT_x10_y-3 etc). Tile world
// origin in cm = (tile_x * 100000, tile_y * 100000).
var tileXYRE = regexp.MustCompile(`_x(-?\d+)_y(-?\d+)$`)

func parseTileXY(base string) (x, y int, ok bool) {
	m := tileXYRE.FindStringSubmatch(base)
	if m == nil {
		return 0, 0, false
	}
	xv, e1 := strconv.Atoi(m[1])
	yv, e2 := strconv.Atoi(m[2])
	if e1 != nil || e2 != nil {
		return 0, 0, false
	}
	return xv, yv, true
}
