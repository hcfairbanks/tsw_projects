package uasset

import (
	"os"
	"reflect"
	"testing"
)

// TestParseCookedRVD pins the cooked RVD path against the JSON path on
// a real freight RVD. Skips when the fixture / sidecar isn't available.
func TestParseCookedRVD(t *testing.T) {
	const ua = `C:\Users\hcfai\Desktop\applications_2\map_files\hsc-extract\TS2Prototype\Plugins\DLC\HSC_50ftBoxCar\Content\RVD_CSX_50ftBoxCar.uasset`
	const jp = `C:\Users\hcfai\AppData\Local\Temp\rvd_csx_box.json`
	if _, err := os.Stat(ua); err != nil {
		t.Skipf("uasset fixture missing: %v", err)
	}
	if _, err := os.Stat(jp); err != nil {
		t.Skipf("json sidecar missing: %v", err)
	}
	a, err := ParseRVD(jp)
	if err != nil {
		t.Fatalf("ParseRVD: %v", err)
	}
	b, err := ParseCookedRVD(ua)
	if err != nil {
		t.Fatalf("ParseCookedRVD: %v", err)
	}
	a.AssetPath = ""
	b.AssetPath = ""
	if !reflect.DeepEqual(a, b) {
		t.Errorf("RVD mismatch\n JSON: %+v\nCOOKED: %+v", a, b)
	}
	t.Logf("class=%q friendly=%q livery=%q cat=%q drivable=%v len=%.1fm regions=%v",
		b.RailVehicleClass, b.FriendlyName, b.LiveryID, b.VehicleCategory,
		b.Drivable, b.ApproximateLenM, b.Regions)
}
