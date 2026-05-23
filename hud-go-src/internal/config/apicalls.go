package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// API calls live in their own file (resources/api_calls.json), separate from
// configuration.json, so the call catalog can grow independently of app
// settings. Calls are organised into named sections ("Core", "German Safety",
// or any section a user creates) — see ApiCallsFile.

// ApiCall is a single TSW CommAPI subscription.
//
//   - Path    the CommAPI node path, e.g. "CurrentDrivableActor.Function.HUD_GetSpeed"
//             or a nested component like
//             "CurrentDrivableActor/PZB_Service_V3.Property.bIsPZB_Active".
//             Stored WITHOUT the "/subscription/" prefix or "?Subscription=1"
//             suffix — subscriptions.go wraps it.
//   - Key     for calls in a non-builtin (user) section, the field name the raw
//             Values object is exposed under in the /stream payload. Calls in a
//             builtin section have bespoke parsing in telemetry.go and ignore Key.
//   - Enabled whether to register the subscription.
type ApiCall struct {
	Path    string `json:"path"`
	Key     string `json:"key"`
	Label   string `json:"label"`
	Enabled bool   `json:"enabled"`
}

// ApiCallSection is a named group of calls. Builtin sections ("Core", "German
// Safety") ship with the app: they're merged into the user's file on load and
// their calls are specially parsed by telemetry.go (so they can be toggled but
// the section itself isn't user-deletable). Non-builtin sections are fully
// user-managed and their calls stream generically by Key.
type ApiCallSection struct {
	Name    string    `json:"name"`
	Builtin bool      `json:"builtin"`
	Calls   []ApiCall `json:"calls"`
}

// ApiCallsFile is the on-disk shape of api_calls.json.
type ApiCallsFile struct {
	Sections []ApiCallSection `json:"sections"`
}

// coreSectionName is the one section that telemetry.go parses into named
// fields (speed, limit, doors, …). Every other section is forwarded raw — see
// PassthroughKeyByPath.
const coreSectionName = "Core"

// legacyApiCall is the pre-split flat shape that used to live in
// configuration.json under "apiCalls". Kept only for one-time migration into
// the sectioned api_calls.json (see migrateLegacyApiCalls).
type legacyApiCall struct {
	Path    string `json:"path"`
	Key     string `json:"key"`
	Label   string `json:"label"`
	Enabled bool   `json:"enabled"`
	Builtin bool   `json:"builtin"`
	Group   string `json:"group"`
}

var (
	apiCallsMu sync.RWMutex
	apiCalls   *ApiCallsFile
)

// ApiCallsPath returns the path to api_calls.json (next to configuration.json).
func ApiCallsPath() string {
	return filepath.Join(ResourcesDir(), "api_calls.json")
}

// defaultApiCallSections is the seed set of builtin sections. "Core" reproduces
// the calls the app has always subscribed to; "German Safety" carries the
// BR 442 PZB/SIFA/AFB calls (value shapes in
// tsw_api_reader/discovery_br442/FINDINGS.md). Both are specially parsed in
// telemetry.go. German calls only return data on a German loco (NodeValid is
// false otherwise, so telemetry skips them) — harmless elsewhere.
func defaultApiCallSections() []ApiCallSection {
	coreP := []string{
		"TimeOfDay.Data",
		"DriverAid.Data",
		"DriverAid.PlayerInfo",
		"DriverAid.TrackData",
		"CurrentDrivableActor.Function.HUD_GetSpeed",
		"CurrentDrivableActor.Function.HUD_GetDirection",
		"CurrentDrivableActor.Function.HUD_GetPowerHandle",
		"CurrentDrivableActor.Function.HUD_GetIsSlipping",
		"CurrentDrivableActor.Function.HUD_GetBrakeGauge_1",
		"CurrentDrivableActor.Function.HUD_GetBrakeGauge_2",
		"CurrentDrivableActor.Function.HUD_GetAcceleration",
		"CurrentDrivableActor.Function.HUD_GetSpeedControlTarget",
		"CurrentDrivableActor.Function.HUD_GetMaxPermittedSpeed",
		"CurrentDrivableActor.Function.HUD_GetTractiveEffort",
		"CurrentDrivableActor.Function.HUD_GetElectricBrakeHandle",
		"CurrentDrivableActor.Function.HUD_GetLocomotiveBrakeHandle",
		"CurrentDrivableActor.Function.HUD_GetTrainBrakeHandle",
		"CurrentDrivableActor.Function.HUD_GetIsTractionLocked",
		"CurrentFormation/1/Door_PassengerDoor_BR.Function.GetCurrentOutputValue",
		"CurrentFormation/1/Door_PassengerDoor_BL.Function.GetCurrentOutputValue",
		"CurrentFormation.FormationLength",
		"CurrentDrivableActor/PassengerDoor_FR.Function.GetCurrentInputValue",
		"CurrentDrivableActor/PassengerDoor_FL.Function.GetCurrentInputValue",
		"WeatherManager.Data",
	}
	core := make([]ApiCall, 0, len(coreP))
	for _, p := range coreP {
		core = append(core, ApiCall{Path: p, Enabled: true})
	}

	german := []ApiCall{
		{Path: "CurrentDrivableActor.Function.IS_GetTargetSpeedMS", Key: "afb", Label: "AFB set speed", Enabled: true},
		{Path: "CurrentDrivableActor/PZB_Service_V3.Property.bIsPZB_Active", Key: "pzbActive", Label: "PZB active", Enabled: true},
		{Path: "CurrentDrivableActor/PZB_Service_V3.Property._InEmergency", Key: "pzbEmergency", Label: "PZB emergency brake", Enabled: true},
		{Path: "CurrentDrivableActor/PZB_Service_V3.Property._RequiresAcknowledge", Key: "pzbAcknowledge", Label: "PZB acknowledge required", Enabled: true},
		{Path: "CurrentDrivableActor/PZB_Service_V3.Property.ActiveMode", Key: "pzbMode", Label: "PZB monitoring mode", Enabled: true},
		{Path: "CurrentDrivableActor/PZB_Service_V3.Function.Get_InfluenceState", Key: "pzbInfluence", Label: "PZB magnet influence (1000/500/2000 Hz)", Enabled: true},
		{Path: "CurrentDrivableActor/BP_Sifa_Service.Property.WarningState", Key: "sifaWarning", Label: "SIFA vigilance warning", Enabled: true},
		{Path: "CurrentDrivableActor/BP_Sifa_Service.Property.inPenaltyBrakeApplication", Key: "sifaPenalty", Label: "SIFA penalty brake", Enabled: true},
	}

	return []ApiCallSection{
		{Name: coreSectionName, Builtin: true, Calls: core},
		{Name: "German Safety", Builtin: true, Calls: german},
	}
}

// mergeApiCallDefaults ensures every builtin section + its calls exist in f,
// without disturbing user toggles, custom sections, or custom calls. New
// builtins from an app update are added; existing entries are left as-is.
// Returns true if anything changed (so the caller can persist).
func mergeApiCallDefaults(f *ApiCallsFile) bool {
	changed := false
	for _, def := range defaultApiCallSections() {
		idx := -1
		for i := range f.Sections {
			if f.Sections[i].Name == def.Name {
				idx = i
				break
			}
		}
		if idx == -1 {
			f.Sections = append(f.Sections, def)
			changed = true
			continue
		}
		if !f.Sections[idx].Builtin {
			f.Sections[idx].Builtin = true
			changed = true
		}
		have := make(map[string]bool, len(f.Sections[idx].Calls))
		for _, c := range f.Sections[idx].Calls {
			have[c.Path] = true
		}
		for _, dc := range def.Calls {
			if !have[dc.Path] {
				f.Sections[idx].Calls = append(f.Sections[idx].Calls, dc)
				changed = true
			}
		}
	}
	return changed
}

// loadApiCalls reads api_calls.json (seeding/merging builtin defaults), or — on
// first run after the split — migrates the legacy flat apiCalls array that used
// to live in configuration.json. Stores the result in the package and persists
// if it changed. Called from config.Load.
func loadApiCalls(legacy []legacyApiCall) {
	f := &ApiCallsFile{}
	path := ApiCallsPath()

	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, f)
	} else if len(legacy) > 0 {
		// One-time migration: group the old flat calls by their `group` field.
		f = migrateLegacyApiCalls(legacy)
	}

	changed := mergeApiCallDefaults(f)
	freshFile := false
	if _, err := os.Stat(path); os.IsNotExist(err) {
		freshFile = true
	}

	apiCallsMu.Lock()
	apiCalls = f
	apiCallsMu.Unlock()

	if changed || freshFile {
		_ = SaveApiCalls(f)
	}
}

// migrateLegacyApiCalls turns the pre-split flat []apiCall (grouped by a `group`
// string) into sections, preserving order of first appearance.
func migrateLegacyApiCalls(legacy []legacyApiCall) *ApiCallsFile {
	f := &ApiCallsFile{}
	idxByName := map[string]int{}
	for _, lc := range legacy {
		name := lc.Group
		if name == "" {
			if lc.Builtin {
				name = "Core"
			} else {
				name = "Custom"
			}
		}
		idx, ok := idxByName[name]
		if !ok {
			f.Sections = append(f.Sections, ApiCallSection{
				Name:    name,
				Builtin: lc.Builtin || name == "Core" || name == "German Safety",
			})
			idx = len(f.Sections) - 1
			idxByName[name] = idx
		}
		f.Sections[idx].Calls = append(f.Sections[idx].Calls, ApiCall{
			Path: lc.Path, Key: lc.Key, Label: lc.Label, Enabled: lc.Enabled,
		})
	}
	return f
}

// GetApiCalls returns the in-memory api calls file (read-safe).
func GetApiCalls() *ApiCallsFile {
	apiCallsMu.RLock()
	defer apiCallsMu.RUnlock()
	return apiCalls
}

// SaveApiCalls replaces the in-memory file and writes api_calls.json.
func SaveApiCalls(f *ApiCallsFile) error {
	apiCallsMu.Lock()
	apiCalls = f
	apiCallsMu.Unlock()

	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ApiCallsPath(), data, 0644)
}

// EnabledSubscriptionPaths returns the node paths of every enabled call across
// all sections. subscriptions.go wraps each into a subscription URL.
func EnabledSubscriptionPaths() []string {
	f := GetApiCalls()
	if f == nil {
		return nil
	}
	var paths []string
	for _, s := range f.Sections {
		for _, c := range s.Calls {
			if c.Enabled {
				paths = append(paths, c.Path)
			}
		}
	}
	return paths
}

// PassthroughKeyByPath maps Path→stream key for every enabled call OUTSIDE the
// special-parsed Core section. This is the standard every section except Core
// follows: Core is parsed into named telemetry fields (the legacy HUD
// contract), while German Safety and any user section are forwarded to the
// frontend raw under their key. Falls back to the path when no key is set, so
// no enabled call is ever silently dropped.
func PassthroughKeyByPath() map[string]string {
	f := GetApiCalls()
	m := map[string]string{}
	if f == nil {
		return m
	}
	for _, s := range f.Sections {
		if s.Name == coreSectionName {
			continue
		}
		for _, c := range s.Calls {
			if !c.Enabled {
				continue
			}
			key := c.Key
			if key == "" {
				key = c.Path
			}
			m[c.Path] = key
		}
	}
	return m
}
