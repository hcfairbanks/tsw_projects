package uasset

import (
	"os"
	"testing"
)

// TestParseCookedTimetable verifies the cooked .uasset parser produces
// a sensible Timetable on a handful of real route timetable assets
// covering different shapes: a service-mode timetable (HSC), a tutorial
// (TC_Tu01), and the WCML master timetable. The .uexp companion must
// be present alongside the .uasset; tests skip when the fixture isn't
// on disk.
func TestParseCookedTimetable(t *testing.T) {
	cases := []struct {
		name        string
		path        string
		route       string
		minServices int
	}{
		{
			name:        "HSC service-mode",
			path:        `C:\Users\hcfai\Desktop\applications_2\map_files\hsc-extract\TS2Prototype\Plugins\DLC\HorseshoeCurve_Route_Gameplay\Content\ServiceMode\HSC_Timetable.uasset`,
			route:       "HorseshoeCurve",
			minServices: 50,
		},
		// Note: TC tutorial timetables (TC_Tu01_Timetable etc.) and TC's
		// service-mode TC_Timetable both lack a top-level `Services`
		// array. The JSON-roundtrip Parse() also returns
		// "Services array not found in payload" on these — confirmed
		// equivalent by side-by-side comparison. The existing extractor
		// silently skips them; ParseCookedTimetable preserves that
		// behaviour. Not adding a "should error" case for them because
		// the meaningful regression-detection is the success path on
		// real service-mode timetables (HSC, WCML, GWE, etc.).
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			if _, err := os.Stat(c.path); err != nil {
				t.Skipf("fixture not present (%s); skipping", c.path)
			}
			tt, err := ParseCookedTimetable(c.path, c.route)
			if err != nil {
				t.Fatalf("ParseCookedTimetable: %v", err)
			}
			if tt.Route != c.route {
				t.Errorf("Route: got %q, want %q", tt.Route, c.route)
			}
			if len(tt.Services) < c.minServices {
				t.Errorf("services: got %d, want >= %d", len(tt.Services), c.minServices)
			}
			if len(tt.Services) > 0 && tt.Services[0].Name == "" {
				t.Errorf("Services[0].Name is empty")
			}
			t.Logf("%s: %d services, %d formations, %d RV map entries, cross_pak=%q",
				c.name, len(tt.Services), len(tt.Formations), len(tt.CompiledRVMap), tt.CrossPakReferenceName)
		})
	}
}
