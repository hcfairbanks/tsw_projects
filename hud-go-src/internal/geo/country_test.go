package geo

import "testing"

// TestCountryFromOrigin pins each rule against a known TSW route origin
// (or a representative city for countries we haven't extracted yet).
// Coordinates here come from origins logged by the route-to-geojson CLI
// or from a quick map sanity check; tweak if a real route's origin lands
// outside the box.
func TestCountryFromOrigin(t *testing.T) {
	cases := []struct {
		name     string
		lat, lng float64
		want     string
	}{
		// TSW route origins we've seen from real extractions.
		{"WCML South (London)", 51.3930015564, 0.1688559949, "UK"},
		{"Horseshoe Curve (PA)", 40.3888015747, -78.6812973022, "US"},
		{"Training Centre (DE)", 51.1156883240, 6.2097029686, "DE"},
		// Representative city-centre points for the remaining countries
		// in the box list — overlap-prone ones first.
		{"Amsterdam NL", 52.37, 4.90, "NL"},
		{"Bern CH", 46.95, 7.45, "CH"},
		{"Vienna AT", 48.21, 16.37, "AT"},
		{"Prague CZ", 50.08, 14.43, "CZ"},
		{"Paris FR", 48.86, 2.35, "FR"},
		{"Rome IT", 41.90, 12.50, "IT"},
		{"Toronto CA", 43.65, -79.38, "CA"},
		// Sentinel: not in any rectangle.
		{"origin unset", 0, 0, ""},
		{"middle of Atlantic", 30, -30, ""},
	}
	for _, c := range cases {
		got := CountryFromOrigin(c.lat, c.lng)
		if got != c.want {
			t.Errorf("%s (%.4f, %.4f): got %q, want %q", c.name, c.lat, c.lng, got, c.want)
		}
	}
}
