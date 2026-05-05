package uasset

import (
	"os"
	"reflect"
	"testing"
)

// TestParseCookedRouteDefinition pins the cooked binary path against the
// JSON-roundtrip path on three real route definitions covering the
// shapes we care about: a US route (HSC, country=USA), a UK route
// (WCML / EMKRouteDefinition with a long DisplayName), and a tutorial
// route (TC, country=DE via codename override at extract time — the
// asset itself ships an empty Country).
//
// The test is data-dependent: it skips when the unpacked map_files
// fixtures aren't on disk and when the JSON sidecars haven't been
// produced via UAssetGUI. CI runs would skip cleanly.
func TestParseCookedRouteDefinition(t *testing.T) {
	cases := []struct {
		name     string
		uasset   string
		jsonPath string
	}{
		{
			name:     "HSC",
			uasset:   `C:\Users\hcfai\Desktop\applications_2\map_files\hsc-extract\TS2Prototype\Plugins\DLC\HorseShoeCurve\Content\RouteDefinition\HorseshoeCurveRouteDefinition.uasset`,
			jsonPath: `C:\Users\hcfai\AppData\Local\Temp\hsc_rd.json`,
		},
		{
			name:     "WCML / EMK",
			uasset:   `C:\Users\hcfai\Desktop\applications_2\map_files\wcml-extract\TS2Prototype\Plugins\DLC\EustonMiltonKeynes\Content\RouteDefinition\EMKRouteDefinition.uasset`,
			jsonPath: `C:\Users\hcfai\AppData\Local\Temp\wcml_rd.json`,
		},
		{
			name:     "TC",
			uasset:   `C:\Users\hcfai\Desktop\applications_2\map_files\tc-editor-extract\TS2Prototype\Plugins\DLC\TrainingCentre\Content\RouteDefinition\TrainingCentreRouteDefinition.uasset`,
			jsonPath: `C:\Users\hcfai\AppData\Local\Temp\tc_rd.json`,
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			if _, err := os.Stat(c.uasset); err != nil {
				t.Skipf("uasset fixture missing: %v", err)
			}
			if _, err := os.Stat(c.jsonPath); err != nil {
				t.Skipf("json sidecar missing (run UAssetGUI tojson first): %v", err)
			}
			a, err := ParseRouteDefinition(c.jsonPath)
			if err != nil {
				t.Fatalf("ParseRouteDefinition (JSON): %v", err)
			}
			b, err := ParseCookedRouteDefinition(c.uasset)
			if err != nil {
				t.Fatalf("ParseCookedRouteDefinition: %v", err)
			}
			// AssetPath naturally differs (one is .json, the other .uasset).
			a.AssetPath = ""
			b.AssetPath = ""
			if !reflect.DeepEqual(a, b) {
				t.Errorf("RouteDefinition mismatch\n JSON: %+v\nCOOKED: %+v", a, b)
			}
			t.Logf("%s: DisplayName=%q country=%q stat=%q cross_pak=%q",
				c.name, b.DisplayName, b.CountryCode, b.StatTrackingName, b.CrossPakReferenceName)
		})
	}
}
