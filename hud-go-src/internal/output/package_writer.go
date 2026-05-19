package output

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"hud-go/internal/geo"
	"hud-go/internal/pak"
	"hud-go/internal/pak/uasset"
)

// filenameMaxLen caps the stem (pre-.json) to mirror the reference zip.
// Retained for legacy callers. New descriptive stems use filenameStemMaxLen.
const filenameMaxLen = 50

// filenameStemMaxLen caps the descriptive per-service stem so path + zip
// prefix stays under Windows MAX_PATH (260). Chosen generous but safe.
const filenameStemMaxLen = 200

// platformInfo is a resolved station-with-structure name keyed by ribbon GUID.
// RibbonGUID + RibbonLocation let us compute the spawn point's lat/lng for
// WAIT FOR SERVICE rows (which otherwise have no ribbon reference of their own).
type platformInfo struct {
	Location, Structure, StructureNumber string
	RibbonGUID                           string
	RibbonLocation                       float32
}

// ttContext caches maps derived from a Timetable so we can enrich a service's
// WAIT FOR SERVICE row with its actual starting platform.
type ttContext struct {
	ribbonToPlatform map[string]platformInfo      // ribbon_guid -> where stops name it
	predecessorByEnd map[string]*uasset.Service   // end_of_service_formation -> service
	formationSpawn   map[string]string            // formation_name -> spawn ribbon_guid
	formationByName  map[string]*uasset.Formation // formation_name -> Formation
	rvdByPath        map[string]*uasset.RVD       // canonical asset path -> RVD
	rvIDToClass      map[string]string            // RailVehicleID GUID -> RailVehicleClass
	rvIDToAssetPath  map[string]string            // RailVehicleID GUID -> CompiledRVMap asset path
}

// lookupRVD resolves a CompiledRVMap asset path to its RVD record using the
// same key-stripping convention as lookupRVDClass.
func (ctx *ttContext) lookupRVD(assetPath string) *uasset.RVD {
	if assetPath == "" || len(ctx.rvdByPath) == 0 {
		return nil
	}
	key := assetPath
	if dot := strings.LastIndex(assetPath, "."); dot > 0 {
		slash := strings.LastIndex(assetPath, "/")
		if slash >= 0 && assetPath[slash+1:dot] == assetPath[dot+1:] {
			key = assetPath[:dot]
		}
	}
	if rvd, ok := ctx.rvdByPath[key]; ok {
		return rvd
	}
	if rvd, ok := ctx.rvdByPath[assetPath]; ok {
		return rvd
	}
	// Fallback: suffix / prefix match on a canonicalised path. RVD discovery
	// may have indexed the file by its on-disk path (e.g. "/BPE_MBTA_...
	// /RVD_BPE_MBTA_CabCar") while the timetable references the /Game-
	// prefixed mount path. Try endswith matching both ways.
	for k, v := range ctx.rvdByPath {
		if strings.HasSuffix(k, key) || strings.HasSuffix(key, k) {
			return v
		}
	}
	return nil
}

func buildTTContext(tt *uasset.Timetable) *ttContext {
	ctx := &ttContext{
		ribbonToPlatform: map[string]platformInfo{},
		predecessorByEnd: map[string]*uasset.Service{},
		formationSpawn:   map[string]string{},
		formationByName:  map[string]*uasset.Formation{},
		rvdByPath:        tt.RVDByPath,
		rvIDToClass:      map[string]string{},
		rvIDToAssetPath:  map[string]string{},
	}
	for i := range tt.Formations {
		f := &tt.Formations[i]
		if f.Name == "" {
			continue
		}
		ctx.formationByName[f.Name] = f
		if f.SpawnRibbonGUID != "" {
			ctx.formationSpawn[f.Name] = f.SpawnRibbonGUID
		}
	}
	// Resolve each RailVehicleID (GUID) in CompiledRVMap to a RailVehicleClass
	// via the RVD map. CompiledRVMap values are asset paths like
	//   "/LIRREX_M7/Data/RailVehicleDefinition/RVD_LIRREX_M7-A.RVD_LIRREX_M7-A"
	// Our RVD lookup key is the same prefix up to the ".ClassName" suffix.
	for guid, assetPath := range tt.CompiledRVMap {
		ctx.rvIDToAssetPath[guid] = assetPath
		cls := lookupRVDClass(ctx.rvdByPath, assetPath)
		if cls != "" {
			ctx.rvIDToClass[guid] = cls
		}
	}
	for i := range tt.Services {
		svc := &tt.Services[i]
		if eos := strings.TrimSpace(svc.EndOfServiceFormation); eos != "" && eos != "None" {
			ctx.predecessorByEnd[eos] = svc
		}
		for _, it := range svc.Schedule {
			if it.RibbonGUID == "" || it.Location == "" {
				continue
			}
			// Prefer entries with structure info. A STOP at a ribbon gives us
			// the canonical "Station / Track / 02" form; a GO VIA sometimes
			// only names the station. Only overwrite an existing entry if we
			// now have structure data and the previous entry didn't.
			existing, seen := ctx.ribbonToPlatform[it.RibbonGUID]
			if seen && existing.Structure != "" {
				continue
			}
			if it.Structure == "" && seen {
				continue
			}
			ctx.ribbonToPlatform[it.RibbonGUID] = platformInfo{
				Location:        it.Location,
				Structure:       it.Structure,
				StructureNumber: it.StructureNumber,
			}
		}
	}
	return ctx
}

// resolveStartPlatform figures out where a service spawns, in order of
// preference:
//
//  1. Formation-chain predecessor: the service whose EndOfServiceFormation
//     matches this service's Formation just parked the train — use the last
//     ribbon in its schedule.
//  2. Formation.SpawnLocation: for chain-head services whose Formation is an
//     Initial spawn, the Formation definition itself holds the spawn ribbon.
//  3. Service's own first scheduled ribbon: for AI portal services (which
//     don't have an Initial-type Formation), the first STOP AT LOCATION in
//     their schedule is typically the portal entry point — their effective
//     start location.
//
// In all cases, the resolved ribbon is looked up in ribbonToPlatform (built
// from every STOP / GO VIA instruction in the timetable) to get a full
// "Station / Structure / Number" triple.
func (ctx *ttContext) resolveStartPlatform(svc *uasset.Service) platformInfo {
	formation := strings.TrimSpace(svc.Formation)

	// 1. Predecessor chain.
	if formation != "" && formation != "None" {
		if prev, ok := ctx.predecessorByEnd[formation]; ok {
			for i := len(prev.Schedule) - 1; i >= 0; i-- {
				g := prev.Schedule[i].RibbonGUID
				if g == "" {
					continue
				}
				ribLoc := prev.Schedule[i].RibbonLocation
				if p, ok := ctx.ribbonToPlatform[g]; ok {
					p.RibbonGUID = g
					p.RibbonLocation = ribLoc
					return p
				}
				// Predecessor's parking ribbon isn't named with structure
				// anywhere — but we still know the ribbon itself, so hand
				// that back for coord enrichment even without a location.
				return platformInfo{RibbonGUID: g, RibbonLocation: ribLoc}
			}
		}
	}

	// 2. Formation.SpawnLocation (Initial-formation chain-head).
	if formation != "" && formation != "None" {
		if g := ctx.formationSpawn[formation]; g != "" {
			var ribLoc float32
			if f, ok := ctx.formationByName[formation]; ok {
				ribLoc = f.SpawnRibbonLoc
			}
			if p, ok := ctx.ribbonToPlatform[g]; ok {
				p.RibbonGUID = g
				p.RibbonLocation = ribLoc
				return p
			}
			return platformInfo{RibbonGUID: g, RibbonLocation: ribLoc}
		}
	}

	// 3. First schedule item with a ribbon (AI portal services and similar).
	for _, it := range svc.Schedule {
		if it.RibbonGUID == "" {
			continue
		}
		if p, ok := ctx.ribbonToPlatform[it.RibbonGUID]; ok {
			p.RibbonGUID = it.RibbonGUID
			p.RibbonLocation = it.RibbonLocation
			return p
		}
		// Even if we have an entry without structure, use it — it's still
		// more accurate than "Start".
		if it.Location != "" {
			return platformInfo{
				Location:        it.Location,
				Structure:       it.Structure,
				StructureNumber: it.StructureNumber,
				RibbonGUID:      it.RibbonGUID,
				RibbonLocation:  it.RibbonLocation,
			}
		}
		break
	}
	return platformInfo{}
}

// lookupRVDClass finds the RailVehicleClass for a CompiledRVMap value. The
// value is a full asset path like "/LIRREX_M7/Data/.../RVD_LIRREX_M7-A.RVD_LIRREX_M7-A";
// our canonicalRVDPath stripped the trailing ".<ObjectName>" part so the
// map key is "/LIRREX_M7/Data/.../RVD_LIRREX_M7-A". We try both the full path
// and the path with the ".<ObjectName>" suffix stripped.
func lookupRVDClass(rvdMap map[string]*uasset.RVD, assetPath string) string {
	if len(rvdMap) == 0 || assetPath == "" {
		return ""
	}
	// Strip everything after the last '.' if that suffix matches the basename.
	// e.g. "/LIRREX_M7/.../RVD_LIRREX_M7-A.RVD_LIRREX_M7-A" -> ".../RVD_LIRREX_M7-A"
	key := assetPath
	if dot := strings.LastIndex(assetPath, "."); dot > 0 {
		slash := strings.LastIndex(assetPath, "/")
		if slash >= 0 && assetPath[slash+1:dot] == assetPath[dot+1:] {
			key = assetPath[:dot]
		}
	}
	if rvd, ok := rvdMap[key]; ok && rvd.RailVehicleClass != "" {
		return rvd.RailVehicleClass
	}
	if rvd, ok := rvdMap[assetPath]; ok && rvd.RailVehicleClass != "" {
		return rvd.RailVehicleClass
	}
	return ""
}

// buildFormations returns the service's `trains` array: one entry per drivable
// class the player can pick, ordered alphabetically. The entry whose class
// matches the formation's actual lead RVD FriendlyName is marked IsDefault
// and carries the full per-vehicle consist (including VehicleID GUIDs) so the
// HUD can match the live API's CurrentFormation[i].VehicleID back to this
// service record. Alternative (substitutable) classes are listed with an
// empty Consists array — their alternative lead has no resolvable GUID from
// the pak, but the non-lead VehicleIDs of the default consist are still
// sufficient for identification.
func buildFormations(ctx *ttContext, svc *uasset.Service) []FormationClassEntry {
	out := []FormationClassEntry{}
	formation := strings.TrimSpace(svc.Formation)
	if formation == "" || formation == "None" {
		return out
	}
	f := ctx.formationByName[formation]
	if f == nil || len(f.Vehicles) == 0 {
		return out
	}

	consist := make([]*uasset.RVD, len(f.Vehicles))
	for i, v := range f.Vehicles {
		if path, ok := ctx.rvIDToAssetPath[v.RailVehicleID]; ok {
			consist[i] = ctx.lookupRVD(path)
		}
	}
	leadIdx := leadVehicleIndex(consist, svc)

	// Resolve the per-vehicle consist.
	vehicles := make([]ConsistVehicle, 0, len(f.Vehicles))
	var lengthM float64
	for i, v := range f.Vehicles {
		length := float64(v.MaxLengthM) + float64(v.ExtensionLengthM)
		lengthM += length
		cv := ConsistVehicle{
			VehicleID: v.RailVehicleID,
			LengthM:   round1(length),
			IsLead:    i == leadIdx,
			IsFlipped: v.Flipped,
		}
		if rvd := consist[i]; rvd != nil {
			cv.RailVehicleClass = rvd.RailVehicleClass
			cv.FriendlyName = rvd.FriendlyName
			cv.LiveryID = rvd.LiveryID
			cv.VehicleCategory = rvd.VehicleCategory
			cv.IsElectric = rvd.IsElectric
			cv.MaxSpeedKph = rvd.MaxSpeedKph
			cv.MaxPowerKw = rvd.MaxPowerKw
			cv.ManufacturerName = rvd.ManufacturerName
			cv.EngineDescription = rvd.EngineDescription
			cv.TypeDescription = rvd.TypeDescription
			cv.ThumbnailAssetRef = rvd.ThumbnailAssetRef
			if len(rvd.Electrification) > 0 {
				cv.Electrification = make([]ElectrificationSpec, len(rvd.Electrification))
				for j, e := range rvd.Electrification {
					cv.Electrification[j] = ElectrificationSpec{
						Current: e.Current, PickupSide: e.PickupSide,
						VoltageV: e.VoltageV, FrequencyHz: e.FrequencyHz,
					}
				}
			}
		} else if cls, ok := ctx.rvIDToClass[v.RailVehicleID]; ok {
			cv.RailVehicleClass = cls
		}
		vehicles = append(vehicles, cv)
	}
	lengthM = round1(lengthM)
	defaultConsist := Consist{
		LengthM:  lengthM,
		CarCount: len(f.Vehicles),
		Vehicles: vehicles,
	}

	// Class set: default lead's FriendlyName + any substitutable RVDs that
	// match (LiveryID, VehicleCategory, overlapping regions). Same logic as
	// the old buildTrainClasses; merged in here so we can mark which entry is
	// the default.
	var defaultClass string
	if leadIdx >= 0 && consist[leadIdx] != nil {
		defaultClass = consist[leadIdx].FriendlyName
	}
	classes := map[string]bool{}
	if defaultClass != "" {
		classes[defaultClass] = true
	}
	if leadIdx >= 0 && consist[leadIdx] != nil {
		lead := consist[leadIdx]
		formRegions := map[string]bool{}
		for _, v := range consist {
			if v == nil {
				continue
			}
			for _, r := range v.Regions {
				formRegions[r] = true
			}
		}
		for _, rvd := range ctx.rvdByPath {
			if !rvd.SubstitutableUnit {
				continue
			}
			if rvd.LiveryID != lead.LiveryID || rvd.VehicleCategory != lead.VehicleCategory {
				continue
			}
			if len(formRegions) > 0 && len(rvd.Regions) > 0 {
				overlap := false
				for _, r := range rvd.Regions {
					if formRegions[r] {
						overlap = true
						break
					}
				}
				if !overlap {
					continue
				}
			}
			if rvd.FriendlyName != "" {
				classes[rvd.FriendlyName] = true
			}
		}
	}

	names := make([]string, 0, len(classes))
	for n := range classes {
		names = append(names, n)
	}
	sortStrings(names)

	for _, n := range names {
		entry := FormationClassEntry{Class: n, Consists: []Consist{}}
		if n == defaultClass {
			entry.IsDefault = true
			entry.Consists = []Consist{defaultConsist}
		}
		out = append(out, entry)
	}
	// If we never resolved a class (no RVD coverage), emit one unnamed entry
	// so the consist + GUIDs are still surfaced for HUD matching.
	if len(out) == 0 {
		out = append(out, FormationClassEntry{
			Class:     "",
			IsDefault: true,
			Consists:  []Consist{defaultConsist},
		})
	}
	return out
}

func round1(f float64) float64 {
	return float64(int(f*10+0.5)) / 10.0
}

// leadVehicleIndex picks the player's driving position in a formation.
//
// PlayerDrivableSide=Front -> vehicle 0.
// PlayerDrivableSide=Back  -> vehicle at last index.
// Fallback: first Locomotive or PassengerCabCar at either end; else vehicle 0.
func leadVehicleIndex(consist []*uasset.RVD, svc *uasset.Service) int {
	if len(consist) == 0 {
		return -1
	}
	switch svc.PlayerDrivableSide {
	case "Front":
		return 0
	case "Back":
		return len(consist) - 1
	}
	first, last := consist[0], consist[len(consist)-1]
	isLead := func(r *uasset.RVD) bool {
		if r == nil {
			return false
		}
		return r.VehicleCategory == "Locomotive" || r.VehicleCategory == "PassengerCabCar"
	}
	if isLead(first) {
		return 0
	}
	if isLead(last) {
		return len(consist) - 1
	}
	return 0
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// isConductorService reports whether the game offers the player a "drive
// vs. conduct" choice for this service. Validated across BOW/BP/Morristown
// bot truth (100% precision, ≥99% recall). See conductor_compatible_rule
// memory for the full derivation.
//
// Rule:
//
//   - IsPlayerDrivable & ServiceClass==Passenger: scope. Conductor role only
//     exists on drivable passenger services.
//   - StopAndLoadCount >= 2: rules out equipment moves and deadheads that
//     touch 0-1 loading stops.
//   - Description does NOT contain "non-revenue": authoritative marker on
//     US routes. Harmless on routes with empty descriptions.
//   - ServiceOperator not in {"None","","Amtrak"}: excludes yard/background
//     services (operator unset) and Amtrak intercity services that the game
//     offers driver-only. Non-universal — new long-distance operators on
//     future routes may need additions here.
//   - Consist has at least one guard-capable car (ServiceTypes==3 OR
//     bHasGuardModeControls==true). Tightens the check against stock that
//     never supports conductor mode.
func isConductorService(svc *uasset.Service, ctx *ttContext) bool {
	if !svc.IsPlayerDrivable {
		return false
	}
	// Do NOT filter on LayerName: on Morristown, HOB / MNF / etc. are valid
	// geographic layer names (Hoboken, Midtown Net Flatbush) on revenue
	// services. Filtering != "None" would reject most of the route.
	if svc.ServiceClass != "Passenger" {
		return false
	}
	if svc.StopAndLoadCount < 2 {
		return false
	}
	d := strings.ToLower(svc.Description)
	if strings.Contains(d, "non-revenue") || strings.Contains(d, "non revenue") {
		return false
	}
	switch svc.ServiceOperator {
	case "", "None", "Amtrak":
		return false
	}
	// Consist check: prefer a guard-capable car (ServiceTypes==3 OR
	// bHasGuardModeControls==true). Only reject on "no guard-capable car
	// found" when we have FULL RVD coverage for the consist — otherwise a
	// missing DLC (e.g. BiLevels on Morristown) would force false negatives.
	f := ctx.formationByName[svc.Formation]
	if f == nil || len(f.Vehicles) == 0 {
		return true
	}
	resolved := 0
	for _, v := range f.Vehicles {
		path, ok := ctx.rvIDToAssetPath[v.RailVehicleID]
		if !ok {
			continue
		}
		rvd := ctx.lookupRVD(path)
		if rvd == nil {
			continue
		}
		resolved++
		if rvd.HasGuardControls || rvd.ServiceTypes == 3 {
			return true
		}
	}
	// If we resolved every vehicle and none qualified, confidently reject.
	if resolved == len(f.Vehicles) {
		return false
	}
	// Incomplete RVD coverage — trust the earlier filters (drivable, passenger,
	// stop-and-load, description, operator) rather than reject on missing data.
	return true
}

// BuildPackageService converts our extracted Service (+ its parent Timetable)
// into the shareable PackageService format.
func BuildPackageService(tt *uasset.Timetable, svc *uasset.Service) *PackageService {
	return buildPackageServiceWithCtx(tt, svc, buildTTContext(tt), nil)
}

// buildPackageServiceWithCtx writes one shareable per-service JSON.
//
// `variant` (when non-nil) carries the route-wide grouping data: the union
// of section names across every .uasset binary that contains this service,
// plus the formation/train data from non-canonical pairs. We use it instead
// of just `tt.SectionName` / `tt.Services[i].Formation` so a service that
// appears in multiple binaries (e.g. Boston-Providence Timetable +
// Boston-Providence HSP-46 Timetable) gets both section names AND every
// formation linked when the importer ingests the JSON. Pass nil only for
// legacy single-tt callers; the resulting JSON will then list at most one
// section and one formation.
func buildPackageServiceWithCtx(tt *uasset.Timetable, svc *uasset.Service, ctx *ttContext, variant *serviceVariant) *PackageService {
	startHM := hmOnly(svc.StartTime)
	duration := computeDuration(svc)

	// Prefer DisplayName + Country read from the route's RouteDefinition
	// asset (extractor stamps these on each Timetable). Fall back to the
	// legacy CamelCase split for the display name when the RouteDefinition
	// scan failed (e.g. cargo DLCs that don't ship the parent's
	// RouteDefinition; their countryName / routeName will be empty here
	// and the importer will inherit values from the existing parent route
	// matched via cross_pak_reference_name).
	displayName := tt.RouteDisplayName
	if displayName == "" {
		displayName = RouteDisplayName(tt.Route)
	}
	countryName := CountryNameFromCode(tt.CountryCode)

	ps := &PackageService{
		CountryName:           countryName,
		RouteName:             displayName,
		CrossPakReferenceName: tt.CrossPakReferenceName,
		ServiceName:           buildServiceName(tt, svc, startHM, duration),
		Description:         svc.Description,
		StartTime:           startHM,
		Duration:            duration,
		ServiceType:         svc.ServiceType,
		ConductorCompatible: isConductorService(svc, ctx),
		CurrentServiceName:  buildCurrentServiceName(svc),
		Coordinates:         []ServiceCoord{},
		Markers:             []any{},
		TotalMarkers:        0,
		TotalPoints:         0,
		RecordingMode:       "backend",
		CoordinatesSource:   "backend",
		Source:              svc.Source,
		Playable:            svc.IsPlayerDrivable,
		Hidden:              svc.IsHidden,
	}
	ps.OriginLat = tt.OriginLat
	ps.OriginLng = tt.OriginLng
	// Section names: when a variant is supplied, use its precomputed union
	// (every section that contains this composed service name across all
	// .uasset binaries on the route). Without a variant, fall back to just
	// this tt's own SectionName.
	ps.SectionNames = []string{}
	if variant != nil && len(variant.sectionNames) > 0 {
		ps.SectionNames = append(ps.SectionNames, variant.sectionNames...)
	} else if tt.SectionName != "" {
		ps.SectionNames = []string{tt.SectionName}
	}
	ps.Formations = buildFormations(ctx, svc)
	if f := strings.TrimSpace(svc.Formation); f != "" && f != "None" {
		ps.FormationName = f
	}
	// Pull formation/train data from non-canonical pairs in the same variant
	// (other binaries declaring the same service). Dedupe by formation_name —
	// when two binaries happen to use the same formation it'd just create
	// noise. Skips the canonical pair's own formation since it's already on
	// ps.FormationName / ps.Trains.
	if variant != nil && len(variant.additional) > 0 {
		seen := map[string]struct{}{}
		if ps.FormationName != "" {
			seen[ps.FormationName] = struct{}{}
		}
		extras := make([]AdditionalFormation, 0, len(variant.additional))
		for _, p := range variant.additional {
			if p.svc == nil {
				continue
			}
			f := strings.TrimSpace(p.svc.Formation)
			if f == "" || f == "None" {
				continue
			}
			if _, dup := seen[f]; dup {
				continue
			}
			seen[f] = struct{}{}
			extras = append(extras, AdditionalFormation{
				FormationName: f,
				Formations:    buildFormations(ctx, p.svc),
			})
		}
		if len(extras) > 0 {
			ps.AdditionalFormations = extras
		}
	}
	startPlatform := ctx.resolveStartPlatform(svc)
	// WAIT FOR SERVICE doesn't carry its own ribbon reference. Inject the
	// start-platform ribbon (from the predecessor chain or Formation spawn)
	// so coord enrichment can resolve its lat/lng like any other stop.
	if startPlatform.RibbonGUID != "" && len(svc.Schedule) > 0 {
		first := &svc.Schedule[0]
		if first.Action == "WAIT FOR SERVICE" && first.RibbonGUID == "" {
			first.RibbonGUID = startPlatform.RibbonGUID
			first.RibbonLocation = startPlatform.RibbonLocation
		}
	}
	enrichScheduleLatLng(svc, tt)
	ps.CsvData, ps.Timetable = buildScheduleRows(svc, startPlatform)
	ps.Bound = computeBound(svc)
	// Resolve the service's full physical path along the rail network and
	// stash it in coordinates[] (hud-go's recording schema). Prefer the
	// per-route ribbon map; fall back to the merged Ribbons map when the
	// per-route scan was skipped or empty.
	if tt.OriginLat != 0 || tt.OriginLng != 0 {
		ribs := tt.RouteRibbons
		if len(ribs) == 0 {
			ribs = tt.Ribbons
		}
		if len(ribs) > 0 {
			anchor := geo.NewRouteAnchor(tt.OriginLat, tt.OriginLng)
			// Prefer the pre-baked DataTrack path when available — the
			// game's own per-service (ribbon, fraction) breadcrumb list
			// from the RouteTimetableDataTrack uasset. Falls back to the
			// proximity-based walker for routes without DataTracks
			// (legacy DLCs) or services not represented in the map
			// (rare; deadhead / portal services).
			var coords []ServiceCoord
			if std, ok := tt.ServiceTrackData[svc.Name]; ok && len(std.TrackData) > 0 {
				coords = BuildServicePathFromTrackData(std.TrackData, ribs, tt.Switches, tt.RibbonVertices, anchor)
			}
			if len(coords) == 0 {
				coords = BuildServicePath(svc, ribs, tt.Switches, tt.RibbonVertices, anchor)
			}
			// Decimate to ~5 m sampling before serialising. The path
			// builders emit sub-metre points (ribbon vertex density);
			// renderers can't show that much detail. Saves ~5× on the
			// per-service coords blob — drops the DB's timetable_
			// coordinates table from ~25 GB to ~3.4 GB with the Height
			// field also stripped. Always keeps the first/last point
			// and any Break point so polyline discontinuities still
			// render correctly.
			coords = DecimateCoordsMeters(coords, 5.0)
			if len(coords) > 0 {
				ps.Coordinates = coords
				ps.TotalPoints = len(coords)
			}
		}
	}
	return ps
}

// enrichScheduleLatLng populates Lat/Lng on each ScheduleItem whose ribbon is
// resolvable from the route's tile index.
//
// CRITICAL: when `tt.RibbonVertices` is populated (the rails phase has run),
// stops are snapped to the nearest rail vertex from that map — the SAME
// vertex set the path-builder slices from. Without this, the analytical arc
// walker used for stops disagrees with the rails sampler at clothoid spirals
// (which the rails sampler renders via cubic Hermite while ArcDelta treats
// them as straight chords). The visible symptom is a small gap between a
// stop marker and the start/end of the polyline at curve-transition
// platforms. Snapping to the rails' vertex set keeps the marker on the
// rendered polyline.
//
// Falls back to the legacy analytical walker when RibbonVertices isn't
// populated (legacy code paths or extractor invocations that skip the rails
// phase).
func enrichScheduleLatLng(svc *uasset.Service, tt *uasset.Timetable) {
	if tt == nil || tt.OriginLat == 0 && tt.OriginLng == 0 {
		return
	}
	if len(tt.Ribbons) == 0 {
		return
	}
	anchor := geo.NewRouteAnchor(tt.OriginLat, tt.OriginLng)
	for i := range svc.Schedule {
		it := &svc.Schedule[i]
		if it.RibbonGUID == "" {
			continue
		}
		// Ribbons are keyed by canonical GUID form; normalize the schedule-side
		// GUID (fmtGUID uppercase 8-8-8-8) to the same shape before lookup.
		key := uasset.NormalizeGUID(it.RibbonGUID)
		if key == "" {
			key = it.RibbonGUID
		}
		rib, ok := tt.Ribbons[key]
		if !ok || rib.Length <= 0 {
			continue
		}

		// Preferred path: snap to the rails-builder vertex set so the stop
		// lies exactly on a vertex of the rendered rail polyline.
		if pts := lookupRibbonVerts(rib, tt.RibbonVertices); len(pts) > 0 {
			vIdx := nearestVertexIndex(pts, float64(it.RibbonLocation))
			it.Lat = pts[vIdx][1]
			it.Lng = pts[vIdx][0]
			continue
		}

		// Legacy fallback: analytical walk of the ribbon arc. Drifts from
		// the rails layer at clothoid sections.
		dx, dy := geo.ArcDelta(0, 0, rib.TangentX, rib.TangentY, rib.Radius, float64(it.RibbonLocation))
		originX, originY, _ := ribbonWorldOrigin(rib)
		// Convert to metres east/south of origin. TSW6 stores ribbon
		// world coordinates as south-positive (verified empirically against
		// known station latitudes — all stops along BOW had +Y values in the
		// pak and the route runs south of origin). Our anchor already expects
		// south-positive, so no flip needed.
		worldEastM := (originX + dx) / 100.0
		worldSouthM := (originY + dy) / 100.0
		lat, lng := anchor.WorldToLatLng(worldEastM, worldSouthM)
		it.Lat = lat
		it.Lng = lng
	}
}

// RouteMapWriter renders the `route_<RouteDisplay>.json` content for one
// route. `tt` is the geometry-owning timetable (the first one with ribbons);
// `allTTs` is every timetable parsed for that route. Implementations write
// a JSON document to w and return any error.
type RouteMapWriter func(w io.Writer, tt *uasset.Timetable, allTTs []*uasset.Timetable, opts RailsGeoJSONOptions) error

// WritePackageWith writes the per-route shareable bundle: per-service JSONs
// plus the route-level files (`route_<X>.json` via mapWriter, and the
// ribbons graph metadata). If `out` ends in .zip, output goes into a
// single zip at that path; otherwise `out` is treated as a directory and
// one subfolder per route is created. serviceFilter, when non-empty, keeps
// only services whose Name or FriendlyName contains the substring
// (case-insensitive); the filter is applied at enumeration time and does
// not prune the timetable.Services list. mapWriter is required.
func WritePackageWith(out string, timetables []*uasset.Timetable, serviceFilter string, mapWriter RouteMapWriter) (int, error) {
	if mapWriter == nil {
		return 0, fmt.Errorf("output: WritePackageWith requires a RouteMapWriter")
	}
	if strings.HasSuffix(strings.ToLower(out), ".zip") {
		return writeZip(out, timetables, serviceFilter, mapWriter)
	}
	return writeDir(out, timetables, serviceFilter, mapWriter)
}

// serviceMatches reports whether a service satisfies the --service substring
// filter. Empty filter matches everything.
func serviceMatches(svc *uasset.Service, filter string) bool {
	if filter == "" {
		return true
	}
	f := strings.ToLower(filter)
	return strings.Contains(strings.ToLower(svc.Name), f) ||
		strings.Contains(strings.ToLower(svc.FriendlyName), f)
}

func writeDir(root string, timetables []*uasset.Timetable, serviceFilter string, mapWriter RouteMapWriter) (int, error) {
	byRoute := groupByRoute(timetables)
	ctxByTT := buildAllContexts(timetables)

	count := 0
	for routeName, svcs := range byRoute {
		dir := filepath.Join(root, routeName)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return count, err
		}
		used := map[string]bool{}
		// Gather railsTT + allTTs first so we can run the rails writer BEFORE
		// the per-service writer. The rails writer (cookedmap.WriteRouteMap)
		// populates `tt.RibbonVertices` on every timetable in allTTs; the
		// per-service path-builder slices from those vertex arrays so the
		// service path lies bit-identically on the rendered rails. If we ran
		// services first, RibbonVertices would be empty and BuildServicePath
		// would have to fall back to its own re-sampler — which diverges from
		// the rails layer at clothoid sections.
		var railsTT *uasset.Timetable
		seenTT := map[*uasset.Timetable]bool{}
		allTTs := []*uasset.Timetable{}
		for _, pair := range svcs {
			if pair.tt != nil && !seenTT[pair.tt] {
				seenTT[pair.tt] = true
				allTTs = append(allTTs, pair.tt)
			}
			if railsTT == nil && pair.tt != nil && len(pair.tt.Ribbons) > 0 {
				railsTT = pair.tt
			}
		}
		writeOne := func(name string, fn func(io.Writer) error) error {
			p := filepath.Join(dir, name)
			f, err := os.Create(p)
			if err != nil {
				return err
			}
			werr := fn(f)
			f.Close()
			return werr
		}
		if railsTT != nil {
			if err := writeOne(RouteDataFilename(routeName), func(w io.Writer) error {
				return runRouteMapWriter(w, railsTT, allTTs, mapWriter)
			}); err != nil {
				return count, err
			}
			if err := writeOne(RibbonsMetaFilename(routeName), func(w io.Writer) error {
				_, e := WriteRibbonsMeta(w, railsTT)
				return e
			}); err != nil {
				return count, err
			}
		}
		// Per-service writes — RibbonVertices is now populated.
		// Group routePairs into variants so a service that appears in
		// multiple .uasset binaries collapses into ONE JSON per playable
		// flag (the importer's dedup key). Section + train data from the
		// non-canonical pairs rides along on the canonical's JSON.
		variants := groupServiceVariants(svcs)
		for _, v := range variants {
			if !serviceMatches(v.canonical.svc, serviceFilter) {
				continue
			}
			ps := buildPackageServiceWithCtx(v.canonical.tt, v.canonical.svc, ctxByTT[v.canonical.tt], &v)
			stem := filenameStem(ps)
			name := uniqueName(used, stem)
			used[name] = true
			path := filepath.Join(dir, name+".json")
			f, err := os.Create(path)
			if err != nil {
				return count, err
			}
			err = writeJSON(f, ps)
			f.Close()
			if err != nil {
				return count, err
			}
			count++
		}
	}
	return count, nil
}

// buildAllContexts prebuilds a ttContext for every timetable once so the
// per-service enrichment pass doesn't rebuild these O(N) maps per call.
func buildAllContexts(timetables []*uasset.Timetable) map[*uasset.Timetable]*ttContext {
	m := make(map[*uasset.Timetable]*ttContext, len(timetables))
	for _, tt := range timetables {
		m[tt] = buildTTContext(tt)
	}
	return m
}

// serviceVariantKey buckets routePairs that the importer would treat as
// the same DB row. The composed ServiceName is the importer's dedup key;
// IsPlayerDrivable is added so a playable variant doesn't silently merge
// with a non-playable AI version of the same service name (the user wants
// those kept distinct in the DB).
type serviceVariantKey struct {
	name     string
	playable bool
}

// serviceVariant is one (name, playable) bucket of routePairs that should
// collapse into a single per-service JSON. `canonical` is the first-seen
// pair and drives all the top-level fields (schedule, coords, primary
// formation). `sectionNames` is the deduped + sorted union of every pair's
// SectionName. `additional` are the other pairs whose formation data needs
// to ride along on the JSON so the importer can still link every train
// via timetable_trains.
type serviceVariant struct {
	canonical    routePair
	sectionNames []string
	additional   []routePair
}

// groupServiceVariants buckets every (tt, svc) pair on a route into
// variants the per-service writer should emit, one JSON per variant.
// Two pairs collapse together when both:
//
//	(a) compose to the same ServiceName (mirrors importer's dedup)
//	(b) share IsPlayerDrivable
//
// Returns variants in first-seen order so output is stable across runs.
//
// This replaces a previous "write every pair, let the importer drop
// duplicates" approach that silently lost the second binary's section
// linkage and any non-canonical formation data.
func groupServiceVariants(svcs []routePair) []serviceVariant {
	type bucket struct {
		canonical routePair
		nameSet   map[string]struct{}
		additions []routePair
	}
	buckets := map[serviceVariantKey]*bucket{}
	order := []serviceVariantKey{}
	for _, p := range svcs {
		if p.tt == nil || p.svc == nil {
			continue
		}
		startHM := hmOnly(p.svc.StartTime)
		duration := computeDuration(p.svc)
		key := serviceVariantKey{
			name:     buildServiceName(p.tt, p.svc, startHM, duration),
			playable: p.svc.IsPlayerDrivable,
		}
		b, ok := buckets[key]
		if !ok {
			b = &bucket{
				canonical: p,
				nameSet:   map[string]struct{}{},
			}
			buckets[key] = b
			order = append(order, key)
		} else {
			b.additions = append(b.additions, p)
		}
		if name := strings.TrimSpace(p.tt.SectionName); name != "" {
			b.nameSet[name] = struct{}{}
		}
	}
	out := make([]serviceVariant, 0, len(order))
	for _, key := range order {
		b := buckets[key]
		names := make([]string, 0, len(b.nameSet))
		for n := range b.nameSet {
			names = append(names, n)
		}
		sort.Strings(names)
		out = append(out, serviceVariant{
			canonical:    b.canonical,
			sectionNames: names,
			additional:   b.additions,
		})
	}
	return out
}

func writeZip(zipPath string, timetables []*uasset.Timetable, serviceFilter string, mapWriter RouteMapWriter) (int, error) {
	byRoute := groupByRoute(timetables)
	ctxByTT := buildAllContexts(timetables)

	// If the user provided a generic name but we only have one route, we still
	// write a single zip. For multiple routes we fan out into sibling zips.
	if len(byRoute) == 1 {
		return writeOneZip(zipPath, firstMapEntry(byRoute), ctxByTT, serviceFilter, mapWriter)
	}

	// Multiple routes: ignore the exact zipPath name, put each route in its own
	// file in the same directory.
	dir := filepath.Dir(zipPath)
	total := 0
	for routeName, svcs := range byRoute {
		rp := filepath.Join(dir, ZipFilename(routeName))
		n, err := writeOneZip(rp, routeRun{name: routeName, pairs: svcs}, ctxByTT, serviceFilter, mapWriter)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// runRouteMapWriter dispatches to the caller-supplied RouteMapWriter.
// WritePackageWith ensures non-nil before reaching this point.
func runRouteMapWriter(w io.Writer, tt *uasset.Timetable, allTTs []*uasset.Timetable, mapWriter RouteMapWriter) error {
	return mapWriter(w, tt, allTTs, DefaultRailsOptions())
}

type routePair struct {
	tt  *uasset.Timetable
	svc *uasset.Service
}

type routeRun struct {
	name  string
	pairs []routePair
}

func groupByRoute(timetables []*uasset.Timetable) map[string][]routePair {
	out := map[string][]routePair{}
	for _, tt := range timetables {
		// Prefer the canonical DisplayName the extractor read from the
		// route's RouteDefinition asset (e.g. "WCML South - London Euston
		// to Milton Keynes"). Fall back to the legacy CamelCase split
		// when that wasn't available — e.g. cargo DLC paks that don't
		// ship the parent's RouteDefinition.
		display := tt.RouteDisplayName
		if display == "" {
			display = RouteDisplayName(tt.Route)
		}
		for i := range tt.Services {
			out[display] = append(out[display], routePair{tt: tt, svc: &tt.Services[i]})
		}
	}
	return out
}

func firstMapEntry(m map[string][]routePair) routeRun {
	for k, v := range m {
		return routeRun{name: k, pairs: v}
	}
	return routeRun{}
}

func writeOneZip(zipPath string, run routeRun, ctxByTT map[*uasset.Timetable]*ttContext, serviceFilter string, mapWriter RouteMapWriter) (int, error) {
	if err := os.MkdirAll(filepath.Dir(zipPath), 0o755); err != nil {
		return 0, err
	}
	f, err := os.Create(zipPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	count := 0
	used := map[string]bool{}
	// Gather railsTT + allTTs first so we can run the rails writer BEFORE
	// the per-service writer. The rails writer (cookedmap.WriteRouteMap)
	// populates `tt.RibbonVertices` on every timetable in allTTs; the
	// per-service path-builder slices from those vertex arrays so the
	// service path lies bit-identically on the rendered rails. See writeDir
	// for the full rationale.
	var railsTT *uasset.Timetable
	seenTT := map[*uasset.Timetable]bool{}
	allTTs := []*uasset.Timetable{}
	for _, pair := range run.pairs {
		if pair.tt != nil && !seenTT[pair.tt] {
			seenTT[pair.tt] = true
			allTTs = append(allTTs, pair.tt)
		}
		if railsTT == nil && pair.tt != nil && (len(pair.tt.RouteRibbons) > 0 || len(pair.tt.Ribbons) > 0) {
			railsTT = pair.tt
		}
	}
	if railsTT != nil {
		if err := addRailsToZip(zw, run.name, allTTs, railsTT, mapWriter); err != nil {
			return count, err
		}
	}
	// Per-service writes — RibbonVertices is now populated.
	// Group routePairs into variants so a service that appears in multiple
	// .uasset binaries collapses into ONE JSON per playable flag (the
	// importer's dedup key). Section + train data from the non-canonical
	// pairs rides along on the canonical's JSON.
	variants := groupServiceVariants(run.pairs)
	for _, v := range variants {
		if !serviceMatches(v.canonical.svc, serviceFilter) {
			continue
		}
		ctx := ctxByTT[v.canonical.tt]
		if ctx == nil {
			ctx = buildTTContext(v.canonical.tt)
		}
		ps := buildPackageServiceWithCtx(v.canonical.tt, v.canonical.svc, ctx, &v)
		stem := filenameStem(ps)
		name := uniqueName(used, stem)
		used[name] = true

		hdr := &zip.FileHeader{
			Name:     name + ".json",
			Method:   zip.Deflate,
			Modified: time.Now(),
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return count, err
		}
		if err := writeJSON(w, ps); err != nil {
			return count, err
		}
		count++
	}

	// Train class thumbnails — one PNG per unique (FriendlyName, ThumbnailAssetRef)
	// across this route's services. Lives in the zip at
	// `images/train_classes/<sanitised>.png`. The importer extracts these
	// alongside the per-service JSONs, so receivers without TSW6 paks still
	// get class images on their /train-classes page.
	if err := addClassThumbnailsToZip(zw, run.pairs); err != nil {
		// Non-fatal: a missing texture .uasset shouldn't fail the extract.
		fmt.Fprintf(os.Stderr, "[zip] WARNING: thumbnail pack: %v\n", err)
	}

	return count, zw.Close()
}

// addClassThumbnailsToZip walks every workdir in the route's pak set,
// parses each RVD_*.uasset, and writes its rendered thumbnail PNG into
// the zip at `images/train_classes/<sanitised rail_vehicle_class>.png`.
// The filename matches what `route_<X>.json`'s train_classes[].thumbnail_rel
// advertises, so the importer can pair each class row with its image
// without any pak-side knowledge.
//
// Dedup key is `rail_vehicle_class` — same canonical identity used
// throughout the train-class data flow. RVDs without a resolvable
// thumbnail asset are skipped silently; the receiver UI handles 404.
//
// Intentionally permissive: a partial set of thumbnails is better than
// failing the whole zip over one bad texture.
func addClassThumbnailsToZip(zw *zip.Writer, pairs []routePair) error {
	// Index .uasset paths across every ExtractDir we see. Keep TWO maps:
	//
	//   indexedCanonical — keyed on the pak's canonical asset path
	//     (matches the format an RVD's ThumbnailAssetRef uses, e.g.
	//     "/TTC_Class323/Data/RVD/TTC_Class323_Thumbnail"). This is the
	//     authoritative lookup — when a pak ships two textures with the
	//     same basename in different folders (TTC's Class 323 has
	//     TTC_Class323_Thumbnail.uasset in both Data/RVD/ AND
	//     Data/LiveryEditor/ with different images), only this index
	//     disambiguates them.
	//   indexedByBase — keyed on basename stem alone, used only as a
	//     fallback for RVDs whose ThumbnailAssetRef points to a path
	//     that didn't get indexed canonically (older paks with relocated
	//     assets).
	indexedCanonical := map[string]string{}
	indexedByBase := map[string]string{}
	indexedDirs := map[string]bool{}
	indexDir := func(dir string) {
		if dir == "" || indexedDirs[dir] {
			return
		}
		indexedDirs[dir] = true
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".uasset") {
				return nil
			}
			indexedCanonical[pak.CanonicalRVDPath(path)] = path
			stem := strings.TrimSuffix(filepath.Base(path), ".uasset")
			if _, ok := indexedByBase[stem]; !ok {
				indexedByBase[stem] = path
			}
			return nil
		})
	}

	// Collect unique workdirs across every timetable in the route.
	workdirs := []string{}
	seenWD := map[string]bool{}
	for _, pair := range pairs {
		if pair.tt == nil || pair.tt.ExtractDir == "" || seenWD[pair.tt.ExtractDir] {
			continue
		}
		seenWD[pair.tt.ExtractDir] = true
		workdirs = append(workdirs, pair.tt.ExtractDir)
	}
	for _, wd := range workdirs {
		indexDir(wd)
	}

	// Walk every RVD_*.uasset across the workdirs. Pack one thumbnail
	// per unique rail_vehicle_class — matches CollectTrainClasses' dedup.
	seenClass := map[string]bool{}
	for _, wd := range workdirs {
		_ = filepath.WalkDir(wd, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			base := d.Name()
			if !pak.IsRVDAsset(base) {
				return nil
			}
			rvd, perr := uasset.ParseCookedRVD(path)
			if perr != nil || rvd == nil || rvd.RailVehicleClass == "" || rvd.ThumbnailAssetRef == "" {
				return nil
			}
			if seenClass[rvd.RailVehicleClass] {
				return nil
			}
			seenClass[rvd.RailVehicleClass] = true
			// Canonical-path lookup first; basename fallback for legacy
			// refs whose canonical form doesn't match what's on disk.
			ref := rvd.ThumbnailAssetRef
			if i := strings.LastIndex(ref, "."); i >= 0 {
				ref = ref[:i]
			}
			assetPath, ok := indexedCanonical[ref]
			if !ok {
				stem := stemFromAssetRef(rvd.ThumbnailAssetRef)
				assetPath, ok = indexedByBase[stem]
			}
			if !ok {
				return nil
			}
			// Filename keyed on FriendlyName, not RailVehicleClass — two
			// paks regularly ship distinct trains with the same rvc stem
			// (TTC's Class323 vs another DLC's Class323), and rvc-keyed
			// filenames collide. FriendlyName differentiates ("Class 323
			// TTC" vs "Class 323"). Matches what the catalog scan's
			// renderRVDThumbnails uses, so the disk file the catalog
			// writes is the same one the importer extracts here.
			fname := rvd.FriendlyName
			if fname == "" {
				fname = rvd.RailVehicleClass
			}
			zipName := "images/train_classes/" + sanitiseClassFilename(fname) + ".png"
			if werr := writeThumbnailToZip(zw, zipName, assetPath); werr != nil {
				fmt.Fprintf(os.Stderr, "[zip] thumbnail %s: %v\n", rvd.RailVehicleClass, werr)
			}
			return nil
		})
	}
	return nil
}

// stemFromAssetRef extracts the .uasset stem from a SoftObjectProperty
// ref string like "/IOW_BR_Class483/Data/.../TSW_483Loco_DLC_Thumbnail.TSW_483Loco_DLC_Thumbnail".
// Returns the asset name part (after last '/' and before any '.AssetName').
func stemFromAssetRef(ref string) string {
	if i := strings.LastIndex(ref, "."); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		return ref[i+1:]
	}
	return ref
}

// sanitiseClassFilename mirrors pak.SanitiseThumbnailName / catalog
// .ThumbnailURLPath so receivers can predict the path without checking
// disk. Same rules: keep [A-Za-z0-9-], turn space/underscore into '_',
// drop the rest.
func sanitiseClassFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r == ' ' || r == '_':
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

// writeThumbnailToZip renders one .uasset texture to PNG bytes and
// writes them as a zip entry. Uses a temp file because
// ExtractTexture2DPNG writes to disk and we need to read it back into
// the zip writer; cleaned up before return.
func writeThumbnailToZip(zw *zip.Writer, zipName, assetPath string) error {
	tmpDir := os.TempDir()
	tmpFile, err := os.CreateTemp(tmpDir, "thumb-*.png")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)
	if _, err := uasset.ExtractTexture2DPNG(assetPath, tmpPath); err != nil {
		return err
	}
	data, err := os.ReadFile(tmpPath)
	if err != nil {
		return err
	}
	hdr := &zip.FileHeader{Name: zipName, Method: zip.Deflate, Modified: time.Now()}
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// addRailsToZip appends the per-route rail files to an open zip writer:
//   - route_<route>.json    — combined rails + track features + platforms + signals + switches + trains[] + route metadata (the one file viewers/HUD should load)
//   - <route>_ribbons.json  — per-ribbon graph metadata (endpoints + node guids), kept because its shape (JSON map keyed by ribbon GUID) is different from GeoJSON and useful for graph reasoning
func addRailsToZip(zw *zip.Writer, routeDisplay string, tts []*uasset.Timetable, tt *uasset.Timetable, mapWriter RouteMapWriter) error {
	addOne := func(name string, write func(io.Writer) error) error {
		hdr := &zip.FileHeader{
			Name:     name,
			Method:   zip.Deflate,
			Modified: time.Now(),
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		return write(w)
	}
	if err := addOne(RouteDataFilename(routeDisplay), func(w io.Writer) error {
		return runRouteMapWriter(w, tt, tts, mapWriter)
	}); err != nil {
		return err
	}
	if err := addOne(RibbonsMetaFilename(routeDisplay), func(w io.Writer) error {
		_, e := WriteRibbonsMeta(w, tt)
		return e
	}); err != nil {
		return err
	}
	return nil
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// filenameStem builds the descriptive filename stem:
//
//	{current_service_name}__{sanitised_serviceName}_{serviceType}_{source}_{playable_flag}
//
// where playable_flag is "playable" or "not_playable". Colons are stripped
// (not replaced) so "23:18" becomes "2318"; spaces become underscores; other
// Windows-illegal characters are stripped. On collision a "-N" suffix is
// appended by uniqueName. The stem is capped at filenameStemMaxLen so the
// final path stays comfortably under Windows MAX_PATH.
func filenameStem(ps *PackageService) string {
	csn := SanitizeForFilename(ps.CurrentServiceName)
	svc := SanitizeForFilename(ps.ServiceName)
	stype := SanitizeForFilename(ps.ServiceType)
	src := SanitizeForFilename(ps.Source)
	playable := "not_playable"
	if ps.Playable {
		playable = "playable"
	}

	var b strings.Builder
	if csn != "" {
		b.WriteString(csn)
		b.WriteString("__")
	}
	b.WriteString(svc)
	if stype != "" {
		b.WriteByte('_')
		b.WriteString(stype)
	}
	if src != "" {
		b.WriteByte('_')
		b.WriteString(src)
	}
	b.WriteByte('_')
	b.WriteString(playable)

	stem := b.String()
	if len(stem) > filenameStemMaxLen {
		stem = strings.TrimRight(stem[:filenameStemMaxLen], "_")
	}
	return stem
}

// uniqueName appends -N to avoid collisions when the same service name appears
// in multiple sections of the same route.
func uniqueName(used map[string]bool, stem string) string {
	if !used[stem] {
		return stem
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", stem, i)
		if !used[candidate] {
			return candidate
		}
	}
}

// buildServiceName mirrors the reference format:
//
//	"P534 Worcester to Boston South Station 23:18 00:00"
//
// = FriendlyName + " " + startHM + " " + duration.
//
// When the per-service FriendlyName / Name is generic (the Training
// Centre's 12 tutorials all stamp "PlayerService", and many cargo
// AI services stamp "AI_Service"), the canonical scenario DisplayName
// from the sibling `<X>_Definition.uasset` is used as the base
// instead. That keeps each scenario's services distinguishable in
// listings and prevents the importer's (service_name, route_id)
// dedup from collapsing them onto a single row.
func buildServiceName(tt *uasset.Timetable, svc *uasset.Service, startHM, duration string) string {
	base := svc.FriendlyName
	if base == "" {
		base = svc.Name
	}
	if tt != nil && tt.ScenarioDisplayName != "" && isGenericServiceName(base) {
		base = tt.ScenarioDisplayName
	}
	var b strings.Builder
	b.WriteString(base)
	if startHM != "" {
		b.WriteByte(' ')
		b.WriteString(startHM)
	}
	if duration != "" {
		b.WriteByte(' ')
		b.WriteString(duration)
	}
	return b.String()
}

// isGenericServiceName flags placeholder service names that don't
// usefully distinguish one timetable from another. Training Centre
// tutorials all use "PlayerService"; many cargo packs use "AI_Service".
// When a generic name appears AND a scenario Definition is available,
// the scenario's DisplayName takes over as the user-facing label.
func isGenericServiceName(name string) bool {
	if name == "" {
		return true
	}
	for _, prefix := range []string{
		"PlayerService", "PlayerFormation", "PlayerTrain",
		"AI_Service", "AI_Train", "AI_Formation",
	} {
		if name == prefix || strings.HasPrefix(name, prefix+"_") || strings.HasPrefix(name, prefix+" ") {
			return true
		}
	}
	return false
}

// buildCurrentServiceName returns the backend service ID that matches the
// bot's `location.current_service_name`. This is the raw Name field from the
// timetable binary — short form on older routes ("MBTA-508") or the long
// form on Gen8-adjacent routes ("MBTA Franklin #701 (Inbound)"). We do NOT
// synthesise a short ID from operator+number: the bot reads the backend ID
// verbatim, so any substitution would break matching.
func buildCurrentServiceName(svc *uasset.Service) string {
	return svc.Name
}

// hmOnly extracts "HH:MM" from "HH:MM:SS".
func hmOnly(s string) string {
	if len(s) >= 5 && s[2] == ':' {
		return s[:5]
	}
	return s
}

// computeDuration returns "HH:MM" from the first scheduled time to the last.
// Returns "00:00" only when the service genuinely has no schedule data.
//
// Handles the midnight-wrap case: a service starting 09:53 with its last
// schedule entry at 00:00 is a 14h07 trip ending after midnight, not a
// 0-duration trip. We assume the schedule never spans more than one
// calendar day; if end < start, treat end as the next day.
func computeDuration(svc *uasset.Service) string {
	start := parseHMS(hmOnly(svc.StartTime))
	end := -1
	for _, it := range svc.Schedule {
		if t := parseHMS(it.Time1); t > end {
			end = t
		}
		if t := parseHMS(it.Time2); t > end {
			end = t
		}
	}
	if start < 0 || end < 0 {
		return "00:00"
	}
	if end < start {
		end += 24 * 3600
	}
	diff := end - start
	h := diff / 3600
	m := (diff % 3600) / 60
	return fmt.Sprintf("%02d:%02d", h, m)
}

// parseHMS returns seconds-since-midnight for "HH:MM" or "HH:MM:SS". -1 on error.
func parseHMS(s string) int {
	if s == "" {
		return -1
	}
	var h, m, sec int
	n, _ := fmt.Sscanf(s, "%d:%d:%d", &h, &m, &sec)
	if n >= 2 {
		return h*3600 + m*60 + sec
	}
	return -1
}

// buildScheduleRows converts a service's schedule into the csvData / timetable
// pair used in the reference format. startPlatform (if set) provides the
// location/structure/structure_number for the WAIT FOR SERVICE row.
func buildScheduleRows(svc *uasset.Service, startPlatform platformInfo) ([]CsvDataRow, []TimetableRow) {
	sched := svc.Schedule
	csvRows := make([]CsvDataRow, 0, len(sched))
	// Starting location for WAIT FOR SERVICE: prefer the resolved platform,
	// then MapPointA (the service's source station), fall back to "Start".
	startLoc := strings.TrimSpace(startPlatform.Location)
	startStruct := startPlatform.Structure
	startStructNum := startPlatform.StructureNumber
	if startLoc == "" {
		startLoc = strings.TrimSpace(svc.MapPointA)
	}
	if startLoc == "" || startLoc == "None" {
		startLoc = "Start"
	}

	// csvData: one raw row per schedule item, unchanged from the source.
	for i, it := range sched {
		details := it.Details
		if details == "" && it.Action == "STOP AT LOCATION" {
			// Reference format uses "<Location> <Structure> <StructureNumber> - <Time1>".
			var locParts []string
			if it.Location != "" {
				locParts = append(locParts, it.Location)
			}
			if it.Structure != "" {
				locParts = append(locParts, it.Structure)
			}
			if it.StructureNumber != "" {
				locParts = append(locParts, it.StructureNumber)
			}
			locStr := strings.Join(locParts, " ")
			if locStr != "" && it.Time1 != "" {
				details = locStr + " - " + it.Time1
			}
		}

		var latStr, lngStr string
		if it.Lat != 0 || it.Lng != 0 {
			latStr = fmtCoord(it.Lat)
			lngStr = fmtCoord(it.Lng)
		}
		csv := CsvDataRow{
			Action:          it.Action,
			CoordSource:     "backend",
			Details:         details,
			Index:           i,
			Latitude:        latStr,
			Longitude:       lngStr,
			Location:        it.Location,
			Structure:       it.Structure,
			StructureNumber: it.StructureNumber,
			Time1:           it.Time1,
			Time2:           it.Time2,
		}
		if it.Action == "WAIT FOR SERVICE" {
			csv.Location = startLoc
			csv.Structure = startStruct
			csv.StructureNumber = startStructNum
		}
		csvRows = append(csvRows, csv)
	}

	// timetable: collapsed view. Each station gets ONE row with both arrival
	// and departure. Pattern (matches the HUD import reference format):
	//
	//   WAIT FOR SERVICE  → row.arrival   = time2 (scheduled start)
	//                       row.departure = next LOAD PASSENGERS.time1
	//   STOP AT LOCATION  → row.arrival   = time1
	//                       row.departure = next LOAD PASSENGERS.time1
	//   GO VIA LOCATION   → row with empty times (pass-through waypoint)
	//   LOAD PASSENGERS   → skipped (merged into preceding row's departure)
	//   UNLOAD PASSENGERS → skipped (terminal marker)
	//   WAIT              → skipped (mid-run delay, not a station)
	ttRows := make([]TimetableRow, 0, len(sched))
	nextLoadTime := func(start int) string {
		for j := start; j < len(sched); j++ {
			switch sched[j].Action {
			case "LOAD PASSENGERS":
				return sched[j].Time1
			case "STOP AT LOCATION", "GO VIA LOCATION", "WAIT FOR SERVICE":
				return "" // reached the next station before a LOAD — no departure
			}
		}
		return ""
	}
	for i, it := range sched {
		var latStr, lngStr string
		if it.Lat != 0 || it.Lng != 0 {
			latStr = fmtCoord(it.Lat)
			lngStr = fmtCoord(it.Lng)
		}
		switch it.Action {
		case "WAIT FOR SERVICE":
			ttRows = append(ttRows, TimetableRow{
				CoordSource:     "backend",
				Index:           len(ttRows),
				Latitude:        latStr,
				Longitude:       lngStr,
				Location:        startLoc,
				Structure:       startStruct,
				StructureNumber: startStructNum,
				Arrival:         it.Time2,
				Departure:       nextLoadTime(i + 1),
			})
		case "STOP AT LOCATION":
			ttRows = append(ttRows, TimetableRow{
				CoordSource:     "backend",
				Index:           len(ttRows),
				Latitude:        latStr,
				Longitude:       lngStr,
				Location:        it.Location,
				Structure:       it.Structure,
				StructureNumber: it.StructureNumber,
				Arrival:         it.Time1,
				Departure:       nextLoadTime(i + 1),
			})
		case "GO VIA LOCATION":
			ttRows = append(ttRows, TimetableRow{
				CoordSource:     "backend",
				Index:           len(ttRows),
				Latitude:        latStr,
				Longitude:       lngStr,
				Location:        it.Location,
				Structure:       it.Structure,
				StructureNumber: it.StructureNumber,
			})
		}
	}
	return csvRows, ttRows
}

func fmtCoord(v float64) string {
	return strconv.FormatFloat(v, 'f', 7, 64)
}

// computeBound returns the compass direction from the service's first
// scheduled stop to its last (whichever axis dominates the delta). Values
// match the hud-go bound-tag CSS classes: "northbound" / "southbound" /
// "eastbound" / "westbound". Returns nil when no usable coords are present.
func computeBound(svc *uasset.Service) any {
	var first, last *uasset.ScheduleItem
	for i := range svc.Schedule {
		s := &svc.Schedule[i]
		if s.Lat == 0 && s.Lng == 0 {
			continue
		}
		if first == nil {
			first = s
		}
		last = s
	}
	if first == nil || last == nil || first == last {
		return nil
	}
	dLat := last.Lat - first.Lat
	dLng := last.Lng - first.Lng
	if math.Abs(dLat) >= math.Abs(dLng) {
		if dLat > 0 {
			return "northbound"
		}
		return "southbound"
	}
	if dLng > 0 {
		return "eastbound"
	}
	return "westbound"
}
