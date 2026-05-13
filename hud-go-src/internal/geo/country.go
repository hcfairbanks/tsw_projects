package geo

// CountryFromOrigin returns a TSW6 country code (matching the values that
// appear in RouteDefinition.RouteDetails.Country) inferred from a route's
// origin lat/lng. Used as an offline last-resort fallback when both the
// pak data and the codename-override map fail to supply a country code.
//
// Returns "" when the point doesn't fall inside any of the rectangles
// covering countries that TSW ships routes for. Bounding boxes are
// intentionally loose — TSW route origins land well inside the country's
// interior, never on borders, so a few degrees of slack costs nothing
// and absorbs any future near-edge route.
//
// The list is restricted to countries already enumerated in
// internal/output/package_names.go so downstream lookups round-trip.
//
// Order matters: smaller / overlap-prone boxes are checked before the
// larger ones they sit inside (e.g. Switzerland before Germany / France,
// Czech Republic before Germany / Austria, Austria before Germany /
// Italy). The first matching rectangle wins.
func CountryFromOrigin(lat, lng float64) string {
	// Skip the origin-not-set sentinel rather than guessing "off the
	// coast of West Africa" → empty TSW string.
	if lat == 0 && lng == 0 {
		return ""
	}
	for _, c := range countryBoxes {
		if lat >= c.minLat && lat <= c.maxLat && lng >= c.minLng && lng <= c.maxLng {
			return c.code
		}
	}
	return ""
}

type countryBox struct {
	code                           string
	minLat, maxLat, minLng, maxLng float64
}

// Ordered smallest-first / most-overlap-prone-first so an Innsbruck
// origin doesn't get mis-tagged as Germany before Austria gets a look.
var countryBoxes = []countryBox{
	// Netherlands — tightened from the geographic extent: the Limburg
	// panhandle (south of ~51.3°N) reaches further east than the rest of
	// the country and is narrow enough that a single rect would catch
	// adjacent German towns (Mönchengladbach lands at 51.12, 6.21 — TC's
	// origin). Routes truly in southern NL would need a codename
	// override.
	{"NL", 51.35, 53.7, 3.3, 7.1},
	// Switzerland — sits inside Germany + France + Italy + Austria boxes.
	{"CH", 45.7, 47.9, 5.9, 10.6},
	// Austria — overlaps Germany + Italy + Switzerland + Czechia.
	{"AT", 46.3, 49.1, 9.5, 17.3},
	// Czech Republic — overlaps Germany + Austria.
	{"CZ", 48.5, 51.1, 12.0, 18.9},
	// United Kingdom — island, no continental overlap.
	{"UK", 49.5, 61.0, -8.5, 2.0},
	// Germany — after the smaller boxes that sit inside it.
	{"DE", 47.2, 55.1, 5.8, 15.1},
	// France — after Switzerland (which overlaps the eastern strip).
	{"FR", 41.3, 51.2, -5.5, 9.6},
	// Italy — after Austria + Switzerland.
	{"IT", 35.4, 47.1, 6.6, 18.6},
	// Canada — high latitude; comes before US for the small overlap
	// near the border.
	{"CA", 41.7, 70.0, -141.0, -52.6},
	// United States — mainland (TSW doesn't ship Hawaii / Alaska).
	{"US", 24.0, 49.5, -125.0, -66.0},
}
