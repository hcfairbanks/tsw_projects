// Sanity check: Go UTM matches Python reference values for BP and IoW origins.
package main

import (
	"fmt"

	"tsw6-timetable/internal/geo"
)

func main() {
	routes := []struct {
		name               string
		lat, lng           float64
		expectZone         int
		expectE, expectN   float64
		uexp               string
	}{
		{"BP", 42.3519401550293, -71.05528259277344, 19, 330724.1, 4690898.8,
			`C:/Users/hcfai/AppData/Local/Temp/bow_routedata/TS2Prototype/Plugins/DLC/BostonWorcester/Content/Map/BostonWorcesterMap.uexp`},
		{"IoW", 50.678375244140625, -1.1386040449142456, 30, 631509.8, 5615712.8,
			`C:/Users/hcfai/AppData/Local/Temp/iow_routedata/TS2Prototype/Plugins/DLC/IsleOfWight/Content/Map/ISleOfWightMap.uexp`},
	}
	for _, r := range routes {
		anchor := geo.NewRouteAnchor(r.lat, r.lng)
		fmt.Printf("\n=== %s ===\n", r.name)
		fmt.Printf("  origin=(%.8f, %.8f)  zone=%d  E=%.1f  N=%.1f\n",
			anchor.OriginLat, anchor.OriginLng, anchor.Zone, anchor.OriginE, anchor.OriginN)
		dz := anchor.Zone - r.expectZone
		de := anchor.OriginE - r.expectE
		dn := anchor.OriginN - r.expectN
		fmt.Printf("  vs expected: zoneΔ=%d  EΔ=%+.2fm  NΔ=%+.2fm\n", dz, de, dn)
		// Test round-trip
		lat, lng := geo.UTMInverse(anchor.OriginE, anchor.OriginN, anchor.Zone)
		fmt.Printf("  round-trip lat=%.8f  lng=%.8f  lat_err=%.2em  lng_err=%.2em\n",
			lat, lng, (lat-r.lat)*111194, (lng-r.lng)*111194)
		// Also: extract from pak uexp and compare to declared values
		lat2, lng2, err := geo.ExtractOriginFromUExp(r.uexp)
		if err != nil {
			fmt.Printf("  pak extract: ERROR %v\n", err)
		} else {
			fmt.Printf("  pak extract: lat=%.8f  lng=%.8f  Δlat=%+.3em  Δlng=%+.3em\n",
				lat2, lng2, (lat2-r.lat)*111194, (lng2-r.lng)*111194)
		}
	}
}
