package geo

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
)

// ExtractOriginFromUExp scans a persistent-map .uexp file for the route's
// origin latitude/longitude.
//
// TSW stores CentralLatitude and CentralLongitude as two consecutive float32
// values 29 bytes apart (longitude first at offset P, latitude second at
// P+29) near the end of the uexp. This helper matches the pattern without
// needing full .uasset parsing — UAssetGUI fails on TSW's .umap files but
// the grep-based scan is reliable across every route tested.
//
// The returned values match the API's `TimeOfDay.Data.OriginLatitude`
// /`OriginLongitude` byte-for-byte.
//
// We scan from the END of the file because occasional earlier float pairs
// can look like plausible lat/lng (e.g. camera FOVs, vector components)
// whereas the true CentralLatitude/Longitude live within the last KB of the
// uexp on every route observed.
func ExtractOriginFromUExp(uexpPath string) (latDeg, lngDeg float64, err error) {
	data, err := os.ReadFile(uexpPath)
	if err != nil {
		return 0, 0, err
	}
	const gap = 29
	// Walk backwards — the origin pair is always in the tail.
	for i := len(data) - gap - 4; i >= 0; i-- {
		lngBits := binary.LittleEndian.Uint32(data[i : i+4])
		latBits := binary.LittleEndian.Uint32(data[i+gap : i+gap+4])
		lng := math.Float32frombits(lngBits)
		lat := math.Float32frombits(latBits)
		if !plausibleLng(float64(lng)) || !plausibleLat(float64(lat)) {
			continue
		}
		// Reject pairs where either component is suspiciously small (camera
		// FOV etc. can produce 32.0, 100.0 type values that trip the filter).
		if math.Abs(float64(lat)) < 1.0 && math.Abs(float64(lng)) < 1.0 {
			continue
		}
		return float64(lat), float64(lng), nil
	}
	return 0, 0, fmt.Errorf("origin lat/lng not found in %s", uexpPath)
}

func plausibleLat(v float64) bool {
	return v > 25 && v < 72
}

func plausibleLng(v float64) bool {
	return v > -180 && v < 180 && math.Abs(v) > 0.001
}
