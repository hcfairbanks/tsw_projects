package pak

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"hud-go/internal/pak/uasset"
)

// PakHasTimetable reports whether a pak ships any timetable or scenario
// uassets — the two asset kinds the extractor pulls service data from.
//
// We deliberately do NOT also require map tiles. Many "DLC" paks add
// services to a parent route's geography without shipping new tiles:
// wagon packs (WCMLSouthHKAWagonPack), cargo packs (CL-Intermodal,
// CL-Nuclear), and similar add-ons. Filtering on tiles drops those, and
// the user wants every DLC with extractable timetable data to appear as
// its own row in /extractor.
//
// Cost: one `repak list <pak>` invocation. Roughly 0.5–2 s per pak.
// Callers should cache the result — paks rarely change.
func PakHasTimetable(pakPath, repakPath string) (bool, error) {
	if repakPath == "" || pakPath == "" {
		return false, fmt.Errorf("PakHasTimetable: empty pakPath or repakPath")
	}
	out, err := exec.Command(repakPath, "list", pakPath).Output()
	if err != nil {
		return false, fmt.Errorf("repak list %s: %w", pakPath, err)
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		lower := strings.ToLower(sc.Text())
		// Match the same path patterns the extractor's findTimetableAssets
		// uses to discover timetable assets at extract time. Training
		// Centre is the canonical "/training/-only" route — without
		// that match it gets dropped from the route list entirely.
		if strings.HasSuffix(lower, ".uasset") &&
			(strings.Contains(lower, "/timetables/") ||
				strings.Contains(lower, "/scenarios/") ||
				strings.Contains(lower, "/training/")) {
			return true, nil
		}
	}
	return false, sc.Err()
}

// PakRouteDefinition pulls the pak's main RouteDefinition asset
// (e.g. EMKRouteDefinition.uasset for WCML South), runs UAssetGUI on
// it, and returns the parsed DisplayName / CountryCode. Used by the
// /extractor list endpoint to label rows with the canonical user-facing
// name TSW ships in the asset rather than a CamelCase guess.
//
// Cost: one `repak unpack` (a few hundred ms — extracts only the two
// .uasset/.uexp files) plus one UAssetGUI invocation (~0.5–1.5 s on a
// .NET cold start). Callers MUST cache — calling this for ~36 paks is
// roughly a minute of work.
//
// scratchDir is where the asset is unpacked + the JSON dropped. It MUST
// already exist; the caller is responsible for any cleanup. The
// implementation chooses a private subdir under it keyed by pak basename
// so concurrent calls don't collide.
//
// Returns (nil, nil) when the pak ships no <X>RouteDefinition asset
// (cargo / wagon DLCs that piggy-back on a parent route's definition) —
// callers should treat that as "not a real route" and fall back to
// CamelCase. Returns an error only on unexpected I/O / tool failures.
func PakRouteDefinition(pakPath, repakPath, _ /* uassetGUIPath, kept for caller compat */, scratchDir string) (*uasset.RouteDefinition, error) {
	if pakPath == "" || repakPath == "" {
		return nil, fmt.Errorf("PakRouteDefinition: empty pakPath / repakPath")
	}
	if scratchDir == "" {
		return nil, fmt.Errorf("PakRouteDefinition: empty scratchDir")
	}

	// Walk the pak's central directory once to find every candidate
	// asset. The route-level definition always lives in a
	// `Content/Routedefinition/` folder (case varies pak-to-pak —
	// observed: "RouteDefinition", "Routedefinition"), but the basename
	// is inconsistent across legacy paks: WCML South uses
	// `EMKRouteDefinition`, West Somerset uses `WestSomersetDefinition`
	// (no "Route" in the name). Filter on the folder path, not the
	// basename — ParseRouteDefinition rejects non-route-level entries
	// (level/reward thresholds, dynamic UI image assets) by checking
	// for the RouteDetails struct.
	out, err := exec.Command(repakPath, "list", pakPath).Output()
	if err != nil {
		return nil, fmt.Errorf("repak list %s: %w", pakPath, err)
	}
	var candidates []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasSuffix(line, ".uasset") {
			continue
		}
		lower := strings.ToLower(line)
		if !strings.Contains(lower, "/routedefinition/") {
			continue
		}
		candidates = append(candidates, line)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("repak list %s: %w", pakPath, err)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	// Per-pak scratch root so concurrent ListRoutes calls don't collide.
	pakStem := strings.TrimSuffix(filepath.Base(pakPath), filepath.Ext(pakPath))
	root := filepath.Join(scratchDir, "routedef_"+pakStem)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir scratch %s: %w", root, err)
	}

	// Order matters: the main definition's basename usually doesn't
	// have a sub-suffix like "LevelThresholds" / "Rewards" /
	// "RewardLevelData" / "EquipmentSpecs", so try the shortest
	// basenames first to skip the obvious sub-assets.
	for i := 0; i < len(candidates); i++ {
		for j := i + 1; j < len(candidates); j++ {
			if len(filepath.Base(candidates[j])) < len(filepath.Base(candidates[i])) {
				candidates[i], candidates[j] = candidates[j], candidates[i]
			}
		}
	}

	for _, entry := range candidates {
		uexpEntry := strings.TrimSuffix(entry, ".uasset") + ".uexp"
		args := []string{"unpack", "-f", "-o", root, "-i", entry, "-i", uexpEntry, pakPath}
		if err := exec.Command(repakPath, args...).Run(); err != nil {
			continue
		}
		uassetPath := filepath.Join(root, filepath.FromSlash(entry))
		rd, err := uasset.ParseCookedRouteDefinition(uassetPath)
		if err != nil {
			// Not a route-level definition — keep walking.
			continue
		}
		return rd, nil
	}
	return nil, nil
}

// PakDLCDisplayName returns the canonical name for a pak that DOESN'T
// ship a RouteDefinition (cargo / wagon / train DLCs). It reads the
// `Description` field from the pak's main `*_Gameplay.uplugin` JSON
// (extracted via `repak unpack -i`), trims a trailing "Gameplay"
// (with or without leading underscore/space), and validates the result:
//
//   - if the trimmed value contains "_" → it's a vendor codename, not a
//     real display name (e.g. BPE Acela's "BPE_AMTK_Acela"); return ""
//     so the caller falls back to its CamelCase split.
//   - if the trimmed value equals the FriendlyName (trimmed similarly)
//     → also a codename → return "".
//   - otherwise return the trimmed value (e.g. "Cargo Line Intermodal",
//     "EMK AWC Class 390").
//
// Returns ("", nil) if no `_Gameplay.uplugin` is found or the JSON is
// malformed — non-fatal; callers fall through to their next source.
//
// Cost: one `repak unpack` per pak (small file, ~100ms). Cache.
func PakDLCDisplayName(pakPath, repakPath, scratchDir string) (string, error) {
	if pakPath == "" || repakPath == "" {
		return "", fmt.Errorf("PakDLCDisplayName: empty pakPath or repakPath")
	}
	if scratchDir == "" {
		return "", fmt.Errorf("PakDLCDisplayName: empty scratchDir")
	}

	// Find candidate uplugin entries. Prefer the route-level
	// `*_Gameplay.uplugin` over the rolling-stock-only ones (they tend
	// to have the route's parent name in their FriendlyName, less
	// useful for the DLC label).
	out, err := exec.Command(repakPath, "list", pakPath).Output()
	if err != nil {
		return "", fmt.Errorf("repak list %s: %w", pakPath, err)
	}
	var gameplay, others []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasSuffix(strings.ToLower(line), ".uplugin") {
			continue
		}
		base := filepath.Base(line)
		if strings.HasSuffix(strings.ToLower(base), "_gameplay.uplugin") {
			gameplay = append(gameplay, line)
		} else {
			others = append(others, line)
		}
	}
	candidates := append(gameplay, others...)
	if len(candidates) == 0 {
		return "", nil
	}

	pakStem := strings.TrimSuffix(filepath.Base(pakPath), filepath.Ext(pakPath))
	root := filepath.Join(scratchDir, "uplugin_"+pakStem)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("mkdir scratch %s: %w", root, err)
	}

	for _, entry := range candidates {
		args := []string{"unpack", "-f", "-o", root, "-i", entry, pakPath}
		if err := exec.Command(repakPath, args...).Run(); err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(entry)))
		if err != nil {
			continue
		}
		var doc struct {
			FriendlyName string `json:"FriendlyName"`
			Description  string `json:"Description"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			continue
		}
		desc := trimGameplaySuffix(doc.Description)
		friendly := trimGameplaySuffix(doc.FriendlyName)
		if isJunkDLCName(desc, friendly) {
			continue
		}
		return desc, nil
	}
	return "", nil
}

// trimGameplaySuffix removes a trailing " Gameplay" or "_Gameplay" tag
// that TSW's older paks append to both Description and FriendlyName.
// Idempotent.
func trimGameplaySuffix(s string) string {
	s = strings.TrimSpace(s)
	for _, suffix := range []string{"_Gameplay", " Gameplay"} {
		s = strings.TrimSuffix(s, suffix)
	}
	return strings.TrimSpace(s)
}

// isJunkDLCName decides whether a `.uplugin` Description is too
// codename-shaped to surface as a user-facing DLC label. Two checks:
//
//   - underscore-in-body — DTG's older train DLCs put the codename
//     verbatim in Description (e.g. "BPE_AMTK_Acela"). A real display
//     name never contains underscores.
//   - equals FriendlyName — FriendlyName is conventionally the codename
//     (e.g. "SHG_CL-I_Gameplay" → "SHG CL-I"); when Description matches
//     it, Description was also set from the codename and isn't a real
//     name.
//
// Empty inputs are treated as junk so the caller falls through.
func isJunkDLCName(description, friendlyName string) bool {
	if description == "" {
		return true
	}
	if strings.Contains(description, "_") {
		return true
	}
	if friendlyName != "" && description == friendlyName {
		return true
	}
	return false
}
