package output

import (
	"regexp"
	"strings"
	"unicode"
)

// routeDisplayNames maps DLC codenames to the user-facing route names.
// Unmapped codenames fall back to a CamelCase-split best-effort.
var routeDisplayNames = map[string]string{
	"IsleOfWight":                  "Isle of Wight",
	"BostonProvidence":             "Boston Sprinter",
	"BostonProvidenceGameplayPack": "Boston Sprinter",
	"BostonWorcester":              "Boston Worcester",
}

// routeCountries maps DLC codenames to their ISO country display name.
var routeCountries = map[string]string{
	"IsleOfWight":                  "United Kingdom",
	"BostonProvidence":             "United States",
	"BostonProvidenceGameplayPack": "United States",
	"BostonWorcester":              "United States",
}

// RouteDisplayName returns the user-facing route name for a DLC codename.
func RouteDisplayName(code string) string {
	if v, ok := routeDisplayNames[code]; ok {
		return v
	}
	return splitCamelCase(code)
}

// RouteCountry returns the country name for a DLC codename, or "" if unknown.
func RouteCountry(code string) string {
	return routeCountries[code]
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
func ZipFilename(routeName string) string {
	return strings.ReplaceAll(routeName, " ", "_") + "_timetables.zip"
}

// RailsFilename returns the file name used for the merged arc-sampled rails
// GeoJSON bundled inside a route's package zip.
// e.g. "Boston Worcester" -> "Boston_Worcester_rails.geojson"
func RailsFilename(routeName string) string {
	return strings.ReplaceAll(routeName, " ", "_") + "_rails.geojson"
}

// RailsStraightFilename is the endpoints-only (straight-line) variant.
// e.g. "Boston Worcester" -> "Boston_Worcester_rails_straight.geojson"
func RailsStraightFilename(routeName string) string {
	return strings.ReplaceAll(routeName, " ", "_") + "_rails_straight.geojson"
}

// RibbonsMetaFilename is the lightweight per-ribbon graph metadata file.
// e.g. "Boston Worcester" -> "Boston_Worcester_ribbons.json"
func RibbonsMetaFilename(routeName string) string {
	return strings.ReplaceAll(routeName, " ", "_") + "_ribbons.json"
}

// PlatformsFilename is the per-platform Point GeoJSON.
// e.g. "Boston Worcester" -> "Boston_Worcester_platforms.geojson"
func PlatformsFilename(routeName string) string {
	return strings.ReplaceAll(routeName, " ", "_") + "_platforms.geojson"
}

// RouteDataFilename is the combined route file: GeoJSON FeatureCollection
// (rails + track features + platforms + signals + switches) plus
// route-level metadata (origin, country) and a `trains[]` list of every
// unique formation referenced by services. Stored as `.json` rather than
// `.geojson` because the top-level `trains` field is a foreign extension
// to the GeoJSON schema.
// e.g. "Boston Worcester" -> "route_Boston_Worcester.json"
func RouteDataFilename(routeName string) string {
	return "route_" + strings.ReplaceAll(routeName, " ", "_") + ".json"
}

// SignalsFilename is the per-signal Point GeoJSON.
// e.g. "Boston Worcester" -> "Boston_Worcester_signals.geojson"
func SignalsFilename(routeName string) string {
	return strings.ReplaceAll(routeName, " ", "_") + "_signals.geojson"
}

// SwitchesFilename is the per-switch Point GeoJSON.
// e.g. "Boston Worcester" -> "Boston_Worcester_switches.geojson"
func SwitchesFilename(routeName string) string {
	return strings.ReplaceAll(routeName, " ", "_") + "_switches.geojson"
}

// TrackFeaturesFilename is the colored-tracks LineString GeoJSON
// (platforms / sidings / lines as separate features for visual overlay).
// e.g. "Boston Worcester" -> "Boston_Worcester_track_features.geojson"
func TrackFeaturesFilename(routeName string) string {
	return strings.ReplaceAll(routeName, " ", "_") + "_track_features.geojson"
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
