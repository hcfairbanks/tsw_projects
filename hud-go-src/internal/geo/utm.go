// Package geo implements the world-to-geographic coordinate conversion that
// TSW6 uses internally: standard UTM (Universal Transverse Mercator) on WGS84
// with k0=0.9996 and false easting 500,000.
//
// The UTM zone is derived from the route's origin longitude (not per-tile or
// per-sample). See memory/world_to_geo_conversion.md for the derivation.
// Validated sub-metre across Boston Providence (zone 19N) and Isle of Wight
// (zone 30N) on 16,098 samples with zero tile-index mismatches.
package geo

import "math"

// WGS84 ellipsoid constants.
const (
	a  = 6378137.0                      // semi-major axis, metres
	f  = 1.0 / 298.257223563            // flattening
	k0 = 0.9996                         // UTM scale factor at central meridian
	fe = 500000.0                       // UTM false easting, metres
)

var (
	e2  = f * (2 - f)
	ep2 = e2 / (1 - e2)
)

// Zone returns the UTM zone for a given longitude in degrees.
func Zone(lngDeg float64) int {
	z := int((lngDeg+180.0)/6.0) + 1
	if z < 1 {
		z = 1
	} else if z > 60 {
		z = 60
	}
	return z
}

// CentralMeridian returns the central meridian longitude (degrees) for a UTM zone.
func CentralMeridian(zone int) float64 {
	return -177.0 + float64(zone-1)*6.0
}

// meridionalArc returns the meridional arc distance (m) from the equator to
// latitude phi (radians) on WGS84.
func meridionalArc(phi float64) float64 {
	return a * ((1-e2/4-3*e2*e2/64-5*math.Pow(e2, 3)/256)*phi -
		(3*e2/8+3*e2*e2/32+45*math.Pow(e2, 3)/1024)*math.Sin(2*phi) +
		(15*e2*e2/256+45*math.Pow(e2, 3)/1024)*math.Sin(4*phi) -
		(35*math.Pow(e2, 3)/3072)*math.Sin(6*phi))
}

// UTMForward converts (latDeg, lngDeg) to UTM easting/northing for the given
// zone. Returns (easting, northing) in metres. Northing is measured from the
// equator (northern hemisphere convention; no 10,000,000 offset).
func UTMForward(latDeg, lngDeg float64, zone int) (easting, northing float64) {
	cm := CentralMeridian(zone)
	B := latDeg * math.Pi / 180
	L := lngDeg * math.Pi / 180
	L0 := cm * math.Pi / 180
	sinB := math.Sin(B)
	cosB := math.Cos(B)
	tanB := math.Tan(B)
	N := a / math.Sqrt(1-e2*sinB*sinB)
	T := tanB * tanB
	C := ep2 * cosB * cosB
	A2 := (L - L0) * cosB
	M := meridionalArc(B)
	x := k0 * N * (A2 +
		(1-T+C)*math.Pow(A2, 3)/6 +
		(5-18*T+T*T+72*C-58*ep2)*math.Pow(A2, 5)/120)
	y := k0 * (M + N*tanB*(A2*A2/2+
		(5-T+9*C+4*C*C)*math.Pow(A2, 4)/24+
		(61-58*T+T*T+600*C-330*ep2)*math.Pow(A2, 6)/720))
	return x + fe, y
}

// UTMInverse converts UTM (easting, northing) in the given zone back to
// (latDeg, lngDeg). Northing assumed from-equator (northern hemisphere).
func UTMInverse(easting, northing float64, zone int) (latDeg, lngDeg float64) {
	cm := CentralMeridian(zone)
	x := easting - fe
	y := northing
	M := y / k0
	mu := M / (a * (1 - e2/4 - 3*e2*e2/64 - 5*math.Pow(e2, 3)/256))
	e1 := (1 - math.Sqrt(1-e2)) / (1 + math.Sqrt(1-e2))
	phi1 := mu + (3*e1/2-27*math.Pow(e1, 3)/32)*math.Sin(2*mu) +
		(21*e1*e1/16-55*math.Pow(e1, 4)/32)*math.Sin(4*mu) +
		(151*math.Pow(e1, 3)/96)*math.Sin(6*mu) +
		(1097*math.Pow(e1, 4)/512)*math.Sin(8*mu)
	sinP := math.Sin(phi1)
	cosP := math.Cos(phi1)
	tanP := math.Tan(phi1)
	C1 := ep2 * cosP * cosP
	T1 := tanP * tanP
	N1 := a / math.Sqrt(1-e2*sinP*sinP)
	R1 := a * (1 - e2) / math.Pow(1-e2*sinP*sinP, 1.5)
	D := x / (N1 * k0)
	lat := phi1 - (N1*tanP/R1)*(D*D/2-
		(5+3*T1+10*C1-4*C1*C1-9*ep2)*math.Pow(D, 4)/24+
		(61+90*T1+298*C1+45*T1*T1-252*ep2-3*C1*C1)*math.Pow(D, 6)/720)
	lng := cm*math.Pi/180 + (D-(1+2*T1+C1)*math.Pow(D, 3)/6+
		(5-2*C1+28*T1-3*C1*C1+8*ep2+24*T1*T1)*math.Pow(D, 5)/120)/cosP
	return lat * 180 / math.Pi, lng * 180 / math.Pi
}

// RouteAnchor holds the per-route origin in both geographic and UTM forms.
type RouteAnchor struct {
	OriginLat   float64 // degrees
	OriginLng   float64 // degrees
	Zone        int     // UTM zone
	OriginE     float64 // origin easting (metres)
	OriginN     float64 // origin northing (metres)
}

// NewRouteAnchor builds a RouteAnchor from origin lat/lng.
func NewRouteAnchor(originLat, originLng float64) *RouteAnchor {
	z := Zone(originLng)
	e, n := UTMForward(originLat, originLng, z)
	return &RouteAnchor{
		OriginLat: originLat,
		OriginLng: originLng,
		Zone:      z,
		OriginE:   e,
		OriginN:   n,
	}
}

// WorldToLatLng converts a world-space position (metres east of origin,
// metres south of origin) to (lat, lng) using the route's UTM anchor.
func (ra *RouteAnchor) WorldToLatLng(worldEastM, worldSouthM float64) (latDeg, lngDeg float64) {
	return UTMInverse(ra.OriginE+worldEastM, ra.OriginN-worldSouthM, ra.Zone)
}

// LatLngToWorld is the inverse of WorldToLatLng — given a lat/lng, returns
// the position in route-local UE world space (metres east, metres south of
// the route's origin). Useful for matching live API geoLocation to ribbon
// world cm without re-extracting the route.
func (ra *RouteAnchor) LatLngToWorld(latDeg, lngDeg float64) (worldEastM, worldSouthM float64) {
	e, n := UTMForward(latDeg, lngDeg, ra.Zone)
	return e - ra.OriginE, ra.OriginN - n
}

// TileCenterToLatLng returns the lat/lng of a tile's centre (1000m tiles, centre
// of tile (0,0) at origin).
func (ra *RouteAnchor) TileCenterToLatLng(tileX, tileY int) (latDeg, lngDeg float64) {
	return ra.WorldToLatLng(float64(tileX)*1000, float64(tileY)*1000)
}

// TileAndOffsetToLatLng converts a tile index + within-tile offset (both
// metres, ranges [-500, +500]) to lat/lng. Useful when reading WorldLocation
// from a tile's umap file (divide UE cm values by 100 to get metres).
func (ra *RouteAnchor) TileAndOffsetToLatLng(tileX, tileY int, withinTileEastM, withinTileSouthM float64) (latDeg, lngDeg float64) {
	return ra.WorldToLatLng(float64(tileX)*1000+withinTileEastM, float64(tileY)*1000+withinTileSouthM)
}
