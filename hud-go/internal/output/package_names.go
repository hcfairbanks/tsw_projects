package output

import (
	"regexp"
	"strings"
	"unicode"
)

// countryCodeNames expands the short country codes that appear in TSW6
// RouteDefinition.RouteDetails.Country (e.g. "UK", "US", "DE") into the
// full country names hud-go uses elsewhere. The codes themselves come
// straight from the pak data; only the expansion is on our side.
//
// Unmapped codes fall back to the code itself.
var countryCodeNames = map[string]string{
	"UK": "United Kingdom",
	"US": "United States",
	"DE": "Germany",
	"FR": "France",
	"AT": "Austria",
	"CH": "Switzerland",
	"IT": "Italy",
	"NL": "Netherlands",
	"CZ": "Czech Republic",
	"CA": "Canada",
}

// CountryNameFromCode expands a TSW6 RouteDetails.Country code (e.g.
// "UK") to the human-readable name ("United Kingdom"). Returns the code
// unchanged if no mapping is known.
func CountryNameFromCode(code string) string {
	if code == "" {
		return ""
	}
	if v, ok := countryCodeNames[code]; ok {
		return v
	}
	return code
}

// tswToISO maps the wildly inconsistent values TSW writers put in
// RouteDetails.Country to their ISO 3166-1 alpha-2 equivalents. The
// raw data ranges across short codes ("UK", "USA"), full English
// names ("United Kingdom", "Germany"), local-language names
// ("Deutschland"), and regional buckets ("North America"). We pass
// the ISO code through to the importer so a route links to the
// existing country row by `code` (which the flag CSS expects) rather
// than by name (which lets stale duplicate rows like "USA" /
// "United States" both win on different imports).
//
// Lookup is case-sensitive — kept that way because the values come
// straight from the asset's CultureInvariantString and DTG ship them
// with stable capitalisation.
//
// "North America" is ambiguous (US + Canada) — defaults to "US"
// (covers 3 of 4 known paks); the Canadian pak (TSW2TorontoIndustrial)
// is handled in codenameFallbackCountry below.
var tswToISO = map[string]string{
	// United Kingdom
	"UK":             "GB",
	"United Kingdom": "GB",
	// United States
	"US":            "US",
	"USA":           "US",
	"United States": "US",
	"North America": "US", // region default; overridden per-codename for CA
	// Germany
	"DE":          "DE",
	"Germany":     "DE",
	"Deutschland": "DE",
	// France
	"FR":     "FR",
	"France": "FR",
	// Switzerland
	"CH":          "CH",
	"Switzerland": "CH",
	// Austria
	"AT":      "AT",
	"Austria": "AT",
	// Italy
	"IT":    "IT",
	"Italy": "IT",
	// Netherlands
	"NL":          "NL",
	"Netherlands": "NL",
	// Czech Republic
	"CZ":             "CZ",
	"Czech Republic": "CZ",
	// Canada
	"CA":     "CA",
	"Canada": "CA",
}

// codenameCountryOverride forces an ISO country code for paks where
// the RouteDefinition's Country is wrong, missing, or too coarse to
// be useful. ALWAYS wins over the data — codename is the authoritative
// signal here.
//
//   - TrainingCentre              empty in data, lives in Germany
//   - EastCoastMainline           empty in data, UK route
//   - MetrolinkAntelopeValleyLine empty in data, LA → US
//   - TSW2TorontoIndustrial       data says "North America"; this is
//                                 the one Canadian route in that
//                                 bucket so we have to override
//                                 explicitly (the regional default
//                                 in tswToISO maps "North America"
//                                 to US, which is right for the
//                                 other 3 paks)
var codenameCountryOverride = map[string]string{
	"TrainingCentre":              "DE",
	"EastCoastMainline":           "GB",
	"MetrolinkAntelopeValleyLine": "US",
	"TSW2TorontoIndustrial":       "CA",
	"TSW2PaddingtonReading":       "GB", // Great Western Express; data ships empty
}

// CountryOverrideForCodename returns a hard-coded ISO country code
// for a pak codename, or "" if no override is known. Callers should
// apply the override AFTER computing the data-derived code so it
// always wins.
func CountryOverrideForCodename(codename string) string {
	return codenameCountryOverride[codename]
}

// CountryISOFromCode normalises a TSW country code to ISO 3166-1
// alpha-2. Returns the input unchanged for codes that are already ISO
// (US, DE, FR, AT, CH, IT, NL, CZ, CA, …) and "" for empty input so
// callers can early-return without an extra check.
func CountryISOFromCode(code string) string {
	if code == "" {
		return ""
	}
	if v, ok := tswToISO[code]; ok {
		return v
	}
	return code
}

// RouteDisplayName is the legacy fallback for routes whose
// <X>RouteDefinition couldn't be loaded — splits CamelCase, e.g.
// "WCMLSouth" → "WCML South". New code paths should prefer the
// DisplayName the extractor reads from the route's RouteDefinition
// (Timetable.RouteDisplayName) and only fall back to this when that is
// empty (cargo DLC zips that don't ship the parent's RouteDefinition).
func RouteDisplayName(code string) string {
	return splitCamelCase(code)
}

// RouteCountry is the legacy fallback. Always empty now — every route's
// country comes from the data via CountryNameFromCode(tt.CountryCode).
// Kept as a no-op stub so older callers that haven't been migrated yet
// still compile.
func RouteCountry(code string) string {
	return ""
}

// splitCamelCase turns "LGVMediterranee" into "LGV Mediterranee" for a
// reasonable fallback display name.
func splitCamelCase(s string) string {
	var b strings.Builder
	runes := []rune(s)
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) && !unicode.IsUpper(runes[i-1]) {
			b.WriteByte(' ')
		} else if i > 0 && i+1 < len(runes) && unicode.IsUpper(r) && unicode.IsUpper(runes[i-1]) && unicode.IsLower(runes[i+1]) {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// filenameSanitiser removes or replaces characters that aren't filename-safe
// and collapses runs of underscores.
var (
	reBadChars = regexp.MustCompile(`[^A-Za-z0-9_]+`)
	reMultiU   = regexp.MustCompile(`_+`)
	reTrimU    = regexp.MustCompile(`^_+|_+$`)
)

// SanitizeFilename turns a serviceName into a filename-safe stem,
// truncated to `maxLen` characters (before the ".json" extension).
//
// Example: "P534 Worcester to Boston South Station 23:18 00:00" ->
// "P534_Worcester_to_Boston_South_Station_23_18_00_00"
func SanitizeFilename(s string, maxLen int) string {
	out := reBadChars.ReplaceAllString(s, "_")
	out = reMultiU.ReplaceAllString(out, "_")
	out = reTrimU.ReplaceAllString(out, "")
	if maxLen > 0 && len(out) > maxLen {
		out = out[:maxLen]
		out = strings.TrimRight(out, "_")
	}
	return out
}

// ZipFilename returns the conventional name used for the shareable zip bundle.
// e.g. "Boston Worcester" -> "Boston_Worcester_timetables.zip"
//
// IMPORTANT: routeName may carry colons / other Windows-illegal chars
// from the canonical DisplayName (e.g. "London Overground: Gospel Oak -
// Barking Riverside"). Naively replacing only spaces leaves the ':' in
// the path, which Windows interprets as the alternate-data-stream
// separator — `os.Create` then writes a 0-byte file at the prefix and
// the actual zip bytes into an invisible ADS. Using SanitizeForFilename
// strips all unsafe chars before assembly.
func ZipFilename(routeName string) string {
	return SanitizeForFilename(routeName) + "_timetables.zip"
}

// RailsFilename returns the file name used for the merged arc-sampled rails
// GeoJSON bundled inside a route's package zip.
// e.g. "Boston Worcester" -> "Boston_Worcester_rails.geojson"
func RailsFilename(routeName string) string {
	return SanitizeForFilename(routeName) + "_rails.geojson"
}

// RailsStraightFilename is the endpoints-only (straight-line) variant.
// e.g. "Boston Worcester" -> "Boston_Worcester_rails_straight.geojson"
func RailsStraightFilename(routeName string) string {
	return SanitizeForFilename(routeName) + "_rails_straight.geojson"
}

// RibbonsMetaFilename is the lightweight per-ribbon graph metadata file.
// e.g. "Boston Worcester" -> "Boston_Worcester_ribbons.json"
func RibbonsMetaFilename(routeName string) string {
	return SanitizeForFilename(routeName) + "_ribbons.json"
}

// PlatformsFilename is the per-platform Point GeoJSON.
// e.g. "Boston Worcester" -> "Boston_Worcester_platforms.geojson"
func PlatformsFilename(routeName string) string {
	return SanitizeForFilename(routeName) + "_platforms.geojson"
}

// RouteDataFilename is the combined route file: GeoJSON FeatureCollection
// (rails + track features + platforms + signals + switches) plus
// route-level metadata (origin, country) and a `trains[]` list of every
// unique formation referenced by services. Stored as `.json` rather than
// `.geojson` because the top-level `trains` field is a foreign extension
// to the GeoJSON schema.
// e.g. "Boston Worcester" -> "route_Boston_Worcester.json"
func RouteDataFilename(routeName string) string {
	return "route_" + SanitizeForFilename(routeName) + ".json"
}

// SignalsFilename is the per-signal Point GeoJSON.
// e.g. "Boston Worcester" -> "Boston_Worcester_signals.geojson"
func SignalsFilename(routeName string) string {
	return SanitizeForFilename(routeName) + "_signals.geojson"
}

// SwitchesFilename is the per-switch Point GeoJSON.
// e.g. "Boston Worcester" -> "Boston_Worcester_switches.geojson"
func SwitchesFilename(routeName string) string {
	return SanitizeForFilename(routeName) + "_switches.geojson"
}

// TrackFeaturesFilename is the colored-tracks LineString GeoJSON
// (platforms / sidings / lines as separate features for visual overlay).
// e.g. "Boston Worcester" -> "Boston_Worcester_track_features.geojson"
func TrackFeaturesFilename(routeName string) string {
	return SanitizeForFilename(routeName) + "_track_features.geojson"
}

// reColon matches a single ':' which we DROP (rather than replace with '_').
// This keeps "23:18 01:35" readable as "2318_0135" instead of "23_18_01_35".
var reColon = regexp.MustCompile(`:`)

// reFilenameIllegal matches characters that are illegal in Windows filenames
// (besides ':' which reColon handles) plus control chars and the path
// separators. These get stripped entirely.
var reFilenameIllegal = regexp.MustCompile(`[<>"/\\|?*\x00-\x1f]+`)

// reSpaceRun collapses whitespace runs to a single underscore.
var reSpaceRun = regexp.MustCompile(`\s+`)

// SanitizeForFilename applies the sanitisation convention used by filenameStem:
//
//   - ':' is removed entirely ("23:18" -> "2318"),
//   - whitespace runs -> single '_',
//   - Windows-illegal chars (< > " / \ | ? *, control chars) are stripped,
//   - runs of '_' are collapsed, leading/trailing '_' trimmed.
//
// Does not truncate — the caller (filenameStem) enforces max length on the
// fully-assembled stem so per-field caps don't clip useful information.
func SanitizeForFilename(s string) string {
	out := reColon.ReplaceAllString(s, "")
	out = reFilenameIllegal.ReplaceAllString(out, "")
	out = reSpaceRun.ReplaceAllString(out, "_")
	out = reMultiU.ReplaceAllString(out, "_")
	out = reTrimU.ReplaceAllString(out, "")
	return out
}
