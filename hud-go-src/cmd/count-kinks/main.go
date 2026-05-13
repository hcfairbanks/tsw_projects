// count-kinks — read a GeoJSON FeatureCollection and report every interior
// polyline vertex whose deflection angle is below a threshold (default 120°).
// Compares MultiLineString rails geometry from tc-hermite output against the
// "smooth track" baseline so we can quantify whether a walker change actually
// reduced visible kinks.
//
// Output: per-input summary (path-count + elbow count by bucket) plus a list
// of the worst N elbows with their lat/lng for cross-reference against the
// 5 known TC kinks reported in the project handoff.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
)

type feature struct {
	Type       string                 `json:"type"`
	Properties map[string]interface{} `json:"properties"`
	Geometry   struct {
		Type        string      `json:"type"`
		Coordinates interface{} `json:"coordinates"`
	} `json:"geometry"`
}

type fc struct {
	Type     string    `json:"type"`
	Features []feature `json:"features"`
}

type kink struct {
	angle    float64 // deg deflection from straight (180° = straight)
	lat, lng float64
	pathIdx  int
	vertIdx  int
}

const earthR = 6371000.0

// llToXY projects lat/lng to a local east/north metres frame anchored at lat0.
// Adequate for the small TC area; angular error vs true tangent is negligible.
func llToXY(lat, lng, lat0 float64) (float64, float64) {
	x := (lng) * (math.Pi / 180) * earthR * math.Cos(lat0*math.Pi/180)
	y := (lat) * (math.Pi / 180) * earthR
	return x, y
}

// vertexAngle returns the interior angle (degrees) at b given polyline a-b-c.
// 180° = perfectly straight; 90° = right-angle elbow. Below 120° = visible kink.
func vertexAngle(a, b, c [2]float64, lat0 float64) float64 {
	ax, ay := llToXY(a[1], a[0], lat0)
	bx, by := llToXY(b[1], b[0], lat0)
	cx, cy := llToXY(c[1], c[0], lat0)
	v1x, v1y := ax-bx, ay-by
	v2x, v2y := cx-bx, cy-by
	n1 := math.Hypot(v1x, v1y)
	n2 := math.Hypot(v2x, v2y)
	if n1 == 0 || n2 == 0 {
		return 180
	}
	cos := (v1x*v2x + v1y*v2y) / (n1 * n2)
	if cos > 1 {
		cos = 1
	}
	if cos < -1 {
		cos = -1
	}
	return math.Acos(cos) * 180 / math.Pi
}

func main() {
	in := flag.String("in", "", "GeoJSON FeatureCollection path")
	threshold := flag.Float64("threshold", 120.0, "report vertices whose interior angle is below this (deg). 90=right-angle elbow.")
	worstN := flag.Int("worst", 20, "list the N sharpest kinks")
	minSegM := flag.Float64("min-seg-m", 1.0, "ignore vertices where either neighbour segment is shorter than this. Tiny segments dominate angle noise without being visible.")
	flag.Parse()
	if *in == "" {
		log.Fatal("missing --in")
	}
	data, err := os.ReadFile(*in)
	if err != nil {
		log.Fatalf("read: %v", err)
	}
	var doc fc
	if err := json.Unmarshal(data, &doc); err != nil {
		log.Fatalf("parse: %v", err)
	}

	// Locate the rails MultiLineString feature (or any LineString).
	var lines [][][2]float64
	for _, f := range doc.Features {
		switch f.Geometry.Type {
		case "MultiLineString":
			coords, ok := f.Geometry.Coordinates.([]interface{})
			if !ok {
				continue
			}
			for _, ln := range coords {
				lns, ok := ln.([]interface{})
				if !ok {
					continue
				}
				var pts [][2]float64
				for _, pt := range lns {
					arr, ok := pt.([]interface{})
					if !ok || len(arr) < 2 {
						continue
					}
					lng, _ := arr[0].(float64)
					lat, _ := arr[1].(float64)
					pts = append(pts, [2]float64{lng, lat})
				}
				if len(pts) >= 2 {
					lines = append(lines, pts)
				}
			}
		case "LineString":
			coords, ok := f.Geometry.Coordinates.([]interface{})
			if !ok {
				continue
			}
			var pts [][2]float64
			for _, pt := range coords {
				arr, ok := pt.([]interface{})
				if !ok || len(arr) < 2 {
					continue
				}
				lng, _ := arr[0].(float64)
				lat, _ := arr[1].(float64)
				pts = append(pts, [2]float64{lng, lat})
			}
			if len(pts) >= 2 {
				lines = append(lines, pts)
			}
		}
	}

	// Use the first vertex's lat as the projection anchor — TC is small enough
	// that any anchor in the area works.
	if len(lines) == 0 {
		log.Fatal("no lines found")
	}
	lat0 := lines[0][0][1]

	var kinks []kink
	totalVerts := 0
	for pi, line := range lines {
		for i := 1; i < len(line)-1; i++ {
			totalVerts++
			a, b, c := line[i-1], line[i], line[i+1]
			ax, ay := llToXY(a[1], a[0], lat0)
			bx, by := llToXY(b[1], b[0], lat0)
			cx, cy := llToXY(c[1], c[0], lat0)
			d1 := math.Hypot(bx-ax, by-ay)
			d2 := math.Hypot(cx-bx, cy-by)
			if d1 < *minSegM || d2 < *minSegM {
				continue
			}
			ang := vertexAngle(a, b, c, lat0)
			if ang < *threshold {
				kinks = append(kinks, kink{ang, b[1], b[0], pi, i})
			}
		}
	}

	fmt.Printf("file: %s\n", *in)
	fmt.Printf("paths: %d  total interior vertices: %d  kinks <%.0f°: %d\n",
		len(lines), totalVerts, *threshold, len(kinks))

	// Bucket by 10° bins to see severity distribution.
	buckets := map[int]int{}
	for _, k := range kinks {
		b := int(k.angle/10) * 10
		buckets[b]++
	}
	var bs []int
	for b := range buckets {
		bs = append(bs, b)
	}
	sort.Ints(bs)
	for _, b := range bs {
		fmt.Printf("  %d-%d°: %d\n", b, b+10, buckets[b])
	}

	sort.Slice(kinks, func(i, j int) bool { return kinks[i].angle < kinks[j].angle })
	fmt.Printf("worst %d kinks:\n", *worstN)
	for i, k := range kinks {
		if i >= *worstN {
			break
		}
		fmt.Printf("  %.2f°  (%.6f, %.6f)  path=%d vert=%d\n",
			k.angle, k.lat, k.lng, k.pathIdx, k.vertIdx)
	}
}
