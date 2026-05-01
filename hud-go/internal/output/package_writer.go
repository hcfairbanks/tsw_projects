package output

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"hud-go/internal/geo"
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

// buildTrains returns the service's `trains` array: one entry per drivable
// class the player can pick, ordered alphabetically. The entry whose class
// matches the formation's actual lead RVD FriendlyName is marked IsDefault
// and carries the full per-vehicle consist (including VehicleID GUIDs) so the
// HUD can match the live API's CurrentFormation[i].VehicleID back to this
// service record. Alternative (substitutable) classes are listed with an
// empty Consists array — their alternative lead has no resolvable GUID from
// the pak, but the non-lead VehicleIDs of the default consist are still
// sufficient for identification.
func buildTrains(ctx *ttContext, svc *uasset.Service) []TrainClassEntry {
	out := []TrainClassEntry{}
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
		entry := TrainClassEntry{Class: n, Consists: []Consist{}}
		if n == defaultClass {
			entry.IsDefault = true
			entry.Consists = []Consist{defaultConsist}
		}
		out = append(out, entry)
	}
	// If we never resolved a class (no RVD coverage), emit one unnamed entry
	// so the consist + GUIDs are still surfaced for HUD matching.
	if len(out) == 0 {
		out = append(out, TrainClassEntry{
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
	return buildPackageServiceWithCtx(tt, svc, buildTTContext(tt))
}

func buildPackageServiceWithCtx(tt *uasset.Timetable, svc *uasset.Service, ctx *ttContext) *PackageService {
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
	ps.SectionNames = []string{}
	if tt.SectionName != "" {
		ps.SectionNames = []string{tt.SectionName}
	}
	ps.Trains = buildTrains(ctx, svc)
	if f := strings.TrimSpace(svc.Formation); f != "" && f != "None" {
		ps.FormationName = f
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
			coords := BuildServicePath(svc, ribs, anchor)
			if len(coords) > 0 {
				ps.Coordinates = coords
				ps.TotalPoints = len(coords)
			}
		}
	}
	return ps
}

// enrichScheduleLatLng populates Lat/Lng on each ScheduleItem whose ribbon is
// resolvable from the route's tile index. Uses the route origin + WGS84 UTM
// inverse (see internal/geo). No-op if the timetable lacks origin or ribbons.
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
		// Walk the arc from the ribbon's true world-space origin
		// (CachedStartPosition on every TSW6 NetworkRibbon). ArcDelta is
		// frame-invariant — only TangentX/Y, Radius, and the arc-length
		// position determine the displacement from the start, so we don't
		// need to know whether the curve's StartX/Y is in world or local
		// coords. RibbonLocation is in UE cm (float32 from the asset).
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

// WritePackage writes a set of timetables as shareable per-service JSON files.
// If `out` ends in .zip, all files go into a single zip at that path.
// Otherwise `out` is treated as a directory; one subfolder per route is created.
func WritePackage(out string, timetables []*uasset.Timetable) (int, error) {
	return WritePackageFiltered(out, timetables, "")
}

// WritePackageFiltered behaves like WritePackage but only emits files for
// services whose Name or FriendlyName contains serviceFilter (case-insensitive).
// Empty filter = emit everything. The filter is applied when enumerating
// output — it does NOT prune the timetable.Services list, so formation-chain
// predecessor resolution and ribbon-to-platform maps still see every service.
func WritePackageFiltered(out string, timetables []*uasset.Timetable, serviceFilter string) (int, error) {
	if strings.HasSuffix(strings.ToLower(out), ".zip") {
		return writeZip(out, timetables, serviceFilter)
	}
	return writeDir(out, timetables, serviceFilter)
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

func writeDir(root string, timetables []*uasset.Timetable, serviceFilter string) (int, error) {
	byRoute := groupByRoute(timetables)
	ctxByTT := buildAllContexts(timetables)

	count := 0
	for routeName, svcs := range byRoute {
		dir := filepath.Join(root, routeName)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return count, err
		}
		used := map[string]bool{}
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
			if !serviceMatches(pair.svc, serviceFilter) {
				continue
			}
			ps := buildPackageServiceWithCtx(pair.tt, pair.svc, ctxByTT[pair.tt])
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
		if railsTT != nil {
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
			if err := writeOne(RouteDataFilename(routeName), func(w io.Writer) error {
				_, e := WriteRouteDataJSON(w, allTTs, DefaultRailsOptions())
				return e
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

func writeZip(zipPath string, timetables []*uasset.Timetable, serviceFilter string) (int, error) {
	byRoute := groupByRoute(timetables)
	ctxByTT := buildAllContexts(timetables)

	// If the user provided a generic name but we only have one route, we still
	// write a single zip. For multiple routes we fan out into sibling zips.
	if len(byRoute) == 1 {
		return writeOneZip(zipPath, firstMapEntry(byRoute), ctxByTT, serviceFilter)
	}

	// Multiple routes: ignore the exact zipPath name, put each route in its own
	// file in the same directory.
	dir := filepath.Dir(zipPath)
	total := 0
	for routeName, svcs := range byRoute {
		rp := filepath.Join(dir, ZipFilename(routeName))
		n, err := writeOneZip(rp, routeRun{name: routeName, pairs: svcs}, ctxByTT, serviceFilter)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
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

func writeOneZip(zipPath string, run routeRun, ctxByTT map[*uasset.Timetable]*ttContext, serviceFilter string) (int, error) {
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
		if !serviceMatches(pair.svc, serviceFilter) {
			continue
		}
		ctx := ctxByTT[pair.tt]
		if ctx == nil {
			ctx = buildTTContext(pair.tt)
		}
		ps := buildPackageServiceWithCtx(pair.tt, pair.svc, ctx)
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
	if railsTT != nil {
		if err := addRailsToZip(zw, run.name, allTTs, railsTT); err != nil {
			return count, err
		}
	}
	return count, zw.Close()
}

// addRailsToZip appends the per-route rail files to an open zip writer:
//   - route_<route>.json    — combined rails + track features + platforms + signals + switches + trains[] + route metadata (the one file viewers/HUD should load)
//   - <route>_ribbons.json  — per-ribbon graph metadata (endpoints + node guids), kept because its shape (JSON map keyed by ribbon GUID) is different from GeoJSON and useful for graph reasoning
func addRailsToZip(zw *zip.Writer, routeDisplay string, tts []*uasset.Timetable, tt *uasset.Timetable) error {
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
		_, e := WriteRouteDataJSON(w, tts, DefaultRailsOptions())
		return e
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
// Returns "00:00" if it can't compute.
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
	if start < 0 || end < 0 || end < start {
		return "00:00"
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
