package uasset

import (
	"os"
	"reflect"
	"testing"
)

// TestParseCookedScenarioDefinition pins the cooked binary path against
// the JSON-roundtrip path on real tutorial Definition assets. Skips when
// the unpacked map_files fixtures or their UAssetGUI sidecars aren't on
// disk.
func TestParseCookedScenarioDefinition(t *testing.T) {
	cases := []struct {
		name     string
		uasset   string
		jsonPath string
	}{
		{
			name:     "TC_Tu01",
			uasset:   `C:\Users\hcfai\Desktop\applications_2\map_files\tc-editor-extract\TS2Prototype\Plugins\DLC\TrainingCentre_Route_Gameplay\Content\Training\RouteIntro\TC_Tu01_Definition.uasset`,
			jsonPath: `C:\Users\hcfai\AppData\Local\Temp\tc_tu01_def.json`,
		},
		{
			name:     "HSC_Intro",
			uasset:   `C:\Users\hcfai\Desktop\applications_2\map_files\hsc-extract\TS2Prototype\Plugins\DLC\HorseshoeCurve_Route_Gameplay\Content\Training\RouteIntro\HSC_Intro_Definition.uasset`,
			jsonPath: `C:\Users\hcfai\AppData\Local\Temp\hsc_intro_def.json`,
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			if _, err := os.Stat(c.uasset); err != nil {
				t.Skipf("uasset fixture missing: %v", err)
			}
			if _, err := os.Stat(c.jsonPath); err != nil {
				t.Skipf("json sidecar missing: %v", err)
			}
			a, err := ParseScenarioDefinition(c.jsonPath)
			if err != nil {
				t.Fatalf("ParseScenarioDefinition (JSON): %v", err)
			}
			b, err := ParseCookedScenarioDefinition(c.uasset)
			if err != nil {
				t.Fatalf("ParseCookedScenarioDefinition: %v", err)
			}
			a.AssetPath = ""
			b.AssetPath = ""
			if !reflect.DeepEqual(a, b) {
				t.Errorf("ScenarioDefinition mismatch\n JSON: %+v\nCOOKED: %+v", a, b)
			}
			t.Logf("%s: DisplayName=%q type=%q desc-len=%d", c.name, b.DisplayName, b.ScenarioType, len(b.Description))
		})
	}
}
