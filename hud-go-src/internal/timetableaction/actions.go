// Package timetableaction holds the canonical action-name constants used
// across the importer, the path-builder, the map-data API, and any other
// code that branches on a schedule entry's action.
//
// Raw string literals were sprinkled through ~9 Go files and several JS
// pages; centralising them here means a typo fails to compile in Go (and
// is grep-able as `timetableaction.Stop` everywhere it matters).
package timetableaction

// Action values stored in `timetable_actions.name` and referenced as
// `ScheduleItem.Action`. The strings match the in-game uasset enum exactly
// — don't tweak casing or wording without checking what writes / reads
// them on the import path.
const (
	// Stop is the regular scheduled stop ("the train stops here, picks up,
	// continues"). Drives platform / car-stop-sign matching.
	Stop = "STOP AT LOCATION"

	// WaitForService is the start-of-service wait — the train is parked at
	// origin and the timetable starts ticking from this row. Behaves as a
	// stop for path-trim and platform-matching purposes.
	WaitForService = "WAIT FOR SERVICE"

	// GoVia is a routing waypoint, not a stop. Influences pathfinding
	// (Dijkstra threads through it) but the polyline shouldn't terminate
	// here and the HUD timetable list flags the row with `[VIA]`.
	GoVia = "GO VIA LOCATION"
)

// IsStop reports whether an action terminates a leg of the polyline — i.e.
// counts as a "real" stop for path-trim and renderable-marker purposes.
// `STOP AT LOCATION` and `WAIT FOR SERVICE` qualify; `GO VIA LOCATION` and
// other actions (UNLOAD PASSENGERS, COUPLE, etc.) do not.
func IsStop(action string) bool {
	return action == Stop || action == WaitForService
}
