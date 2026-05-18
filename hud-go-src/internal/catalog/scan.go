package catalog

import (
	"bufio"
	"bytes"
	"database/sql"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"hud-go/internal/output"
	"hud-go/internal/pak"
)

// Tools is the set of external binaries the catalog scan needs.
type Tools struct {
	RepakPath     string // repak.exe — for unpacking + listing pak contents
	UAssetGUIPath string // UAssetGUI.exe — for parsing RouteDefinition assets
	ScratchDir    string // temp working directory for repak unpacks
	// ThumbnailsDir is where per-class thumbnail PNGs get rendered to
	// during the scan. Typically <appDir>/images/train_classes/. Empty
	// disables thumbnail rendering — RVDs still get parsed.
	ThumbnailsDir string
	// Logger is an optional progress callback. ScanCatalog calls it
	// before each pak with `("Scanning %s (i/total)…", codename)` so
	// callers can pipe per-pak updates into the live extractor log.
	// Nil = no progress events.
	Logger func(format string, args ...any)
}

// ScanResult summarises one ScanCatalog run.
type ScanResult struct {
	Added   int
	Updated int
	Skipped int // unchanged paks (mtime+size match)
	Removed int // paks deleted from disk since last scan
}

// ScanCatalog walks every pak in the install dir and ensures the
// catalog table reflects it. Diff strategy: for each on-disk pak,
// compare (mtime, size) against the catalog row. Identical rows are
// skipped; new or changed paks are re-scanned. Catalog rows whose
// pak_path no longer exists on disk are deleted.
//
// `force=true` re-scans every pak regardless of mtime.
//
// First run on a fresh install with ~36 paks takes 1–3 minutes
// (RouteDefinition + uplugin + cross-pak-ref scan per pak). Subsequent
// runs against an unchanged install complete in milliseconds.
func ScanCatalog(db *sql.DB, tswPath string, tools Tools, force bool) (ScanResult, error) {
	var res ScanResult
	if tools.RepakPath == "" {
		return res, fmt.Errorf("ScanCatalog: missing repak path")
	}
	if tools.ScratchDir == "" {
		return res, fmt.Errorf("ScanCatalog: missing scratch dir")
	}
	if err := os.MkdirAll(tools.ScratchDir, 0o755); err != nil {
		return res, fmt.Errorf("mkdir scratch: %w", err)
	}

	routes, err := pak.DiscoverRoutes(tswPath)
	if err != nil {
		return res, err
	}

	// Build the on-disk pak set so we can detect removed paks at the end.
	onDisk := make(map[string]struct{}, len(routes))
	for _, r := range routes {
		onDisk[r.PakPath] = struct{}{}
	}

	total := len(routes)
	for i, r := range routes {
		st, err := os.Stat(r.PakPath)
		if err != nil {
			log.Printf("[catalog] stat %s: %v", r.PakPath, err)
			continue
		}
		mtime := st.ModTime().Unix()
		size := st.Size()

		existing, _ := Lookup(db, r.PakPath)
		if !force && existing != nil && existing.PakMtime == mtime && existing.PakSize == size {
			res.Skipped++
			if tools.Logger != nil {
				tools.Logger("[catalog %d/%d] %s — unchanged", i+1, total, friendlyLabel(r.Name, existing))
			}
			continue
		}

		if tools.Logger != nil {
			tools.Logger("[catalog %d/%d] scanning %s…", i+1, total, friendlyLabel(r.Name, existing))
		}
		entry, err := ScanPak(r.PakPath, r.Name, tools)
		if err != nil {
			log.Printf("[catalog] scan %s: %v", r.PakPath, err)
			if tools.Logger != nil {
				tools.Logger("[catalog %d/%d] %s — error: %v", i+1, total, friendlyLabel(r.Name, existing), err)
			}
			continue
		}
		entry.PakMtime = mtime
		entry.PakSize = size
		entry.ScannedAt = time.Now().Unix()

		if err := Upsert(db, entry); err != nil {
			log.Printf("[catalog] upsert %s: %v", r.PakPath, err)
			continue
		}
		if existing == nil {
			res.Added++
		} else {
			res.Updated++
		}

		// Per-pak RVDs (rolling stock). Done here so the per-route
		// extraction can skip the slow ScanAllRVDs walk and read from
		// pak_rvds instead. RVD scanning costs ~5–20 s per pak with
		// trains; paks that ship none short-circuit fast.
		if tools.UAssetGUIPath != "" {
			rvds, err := pak.PakRVDsWithThumbnails(r.PakPath, tools.RepakPath, tools.ScratchDir, tools.ThumbnailsDir)
			if err != nil {
				log.Printf("[catalog] PakRVDs(%s): %v", r.PakPath, err)
			} else if err := ReplaceRVDs(db, r.PakPath, rvds); err != nil {
				log.Printf("[catalog] ReplaceRVDs(%s): %v", r.PakPath, err)
			}
			if tools.Logger != nil {
				tools.Logger("[catalog %d/%d] %s — done (%s, %d ref(s), %d RVD(s))",
					i+1, total, friendlyLabel(r.Name, entry), entryKindLabel(entry),
					len(entry.CrossPakRefs), len(rvds))
			}
		} else if tools.Logger != nil {
			tools.Logger("[catalog %d/%d] %s — done (%s, %d ref(s))",
				i+1, total, friendlyLabel(r.Name, entry), entryKindLabel(entry), len(entry.CrossPakRefs))
		}
	}

	// Reap rows whose pak no longer exists on disk.
	all, err := LoadAll(db)
	if err != nil {
		return res, err
	}
	for _, p := range all {
		if _, ok := onDisk[p.PakPath]; ok {
			continue
		}
		if err := Delete(db, p.PakPath); err != nil {
			log.Printf("[catalog] delete %s: %v", p.PakPath, err)
			continue
		}
		res.Removed++
	}

	// Reconcile train_classes against the freshly-populated pak_rvds.
	// Inserts/links/backfills every class found across every pak — even
	// the standalone train DLCs that don't ship a parent route. Removes
	// the need for users to click the "Rebuild Train Classes from RVDs"
	// dev button after a scan: the full class set appears immediately.
	tcRes := ReconcileTrainClasses(db)
	if tools.Logger != nil && (tcRes.Linked+tcRes.Inserted+tcRes.Backfilled) > 0 {
		tools.Logger("[catalog] reconciled train_classes: linked=%d inserted=%d backfilled=%d thumbs_fixed=%d",
			tcRes.Linked, tcRes.Inserted, tcRes.Backfilled, tcRes.ThumbsFixed)
	}
	return res, nil
}

// ScanPak inspects one pak and produces its catalog entry. Three
// passes:
//
//  1. PakRouteDefinition → DisplayName + CountryCode if this pak ships
//     its own RouteDefinition (it's a parent route).
//  2. PakDLCDisplayName  → fallback DisplayName from `*_Gameplay.uplugin`
//     Description (cargo / wagon / train DLCs).
//  3. scanCrossPakRefs   → all cross_pak_reference_names in the pak's
//     timetable / scenario assets. This is the linkage that the tree
//     uses to attach addons under their parent routes.
//
// PakMtime / PakSize / ScannedAt are not set here — the caller stamps
// them after a successful scan.
func ScanPak(pakPath, codename string, tools Tools) (*Pak, error) {
	p := &Pak{
		PakPath:  pakPath,
		Codename: codename,
	}

	if tools.UAssetGUIPath != "" {
		rd, err := pak.PakRouteDefinition(pakPath, tools.RepakPath, tools.UAssetGUIPath, tools.ScratchDir)
		if err != nil {
			log.Printf("[catalog] PakRouteDefinition(%s): %v", pakPath, err)
		}
		if rd != nil {
			p.HasRouteDef = true
			p.DisplayName = rd.DisplayName
			p.CountryCode = rd.CountryCode
			p.CrossPakReferenceName = rd.CrossPakReferenceName
		}
	}

	if !p.HasRouteDef {
		name, err := pak.PakDLCDisplayName(pakPath, tools.RepakPath, tools.ScratchDir)
		if err != nil {
			log.Printf("[catalog] PakDLCDisplayName(%s): %v", pakPath, err)
		}
		if name != "" {
			p.DisplayName = name
		}
	}

	// Normalise to ISO 3166-1 alpha-2 before saving. The raw asset
	// value is wildly inconsistent — short codes ("UK", "USA"), full
	// names ("United Kingdom", "Germany"), regions ("North America"),
	// and locales ("Deutschland"). Storing the ISO form means every
	// downstream consumer (tree, importer, flag CSS) sees the same
	// clean key.
	p.CountryCode = output.CountryISOFromCode(p.CountryCode)

	// Per-codename country override. Wins over whatever the
	// RouteDefinition supplied — the data is unreliable, and the
	// codename is the authoritative key for the few paks where we
	// know the right answer (e.g. TSW2TorontoIndustrial says
	// "North America" but it's the lone Canadian route).
	if override := output.CountryOverrideForCodename(p.Codename); override != "" {
		p.CountryCode = override
	}

	refs, err := scanCrossPakRefs(pakPath, tools.RepakPath, tools.ScratchDir)
	if err != nil {
		log.Printf("[catalog] scanCrossPakRefs(%s): %v", pakPath, err)
	}
	p.CrossPakRefs = refs

	return p, nil
}

// crossPakPattern picks `/<X>/RouteDefinition/` strings out of raw
// .uasset bytes — the same NameMap entries the timetable parser reads.
// `<X>` is the parent route's mount path (e.g. EustonMiltonKeynes,
// WCMLPresCar, IslandLine). The capture group is intentionally narrow:
// alphanumerics + underscore + hyphen, no slashes, no whitespace.
var crossPakPattern = regexp.MustCompile(`/([A-Za-z0-9_\-]+)/RouteDefinition/`)

// scanCrossPakRefs unpacks every timetable-shape uasset under
// /Scenarios/ + /Timetables/ + /Training/ in the pak, then byte-scans
// each one for /<X>/RouteDefinition/ patterns. Pure byte scan — no
// UAssetGUI involved (we don't need parsed properties, only the
// NameMap strings, which appear verbatim in the asset binary).
func scanCrossPakRefs(pakPath, repakPath, scratchDir string) ([]string, error) {
	candidates, err := listTimetableShapeAssets(pakPath, repakPath)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	pakStem := strings.TrimSuffix(filepath.Base(pakPath), filepath.Ext(pakPath))
	root := filepath.Join(scratchDir, "refs_"+pakStem)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir scratch %s: %w", root, err)
	}
	defer os.RemoveAll(root)

	// Run repak unpack in batches. Windows' CreateProcess argv is capped
	// at ~32 KB; large paks (Boston Providence, WCML South) ship hundreds
	// of scenario assets and a single `unpack -i a -i b -i c …` call
	// trips the limit. 4 KB per batch's worth of include args keeps us
	// safely under the cap with comfortable headroom.
	const maxArgsBytes = 4096
	for i := 0; i < len(candidates); {
		args := []string{"unpack", "-f", "-o", root}
		bytesUsed := 0
		for ; i < len(candidates); i++ {
			c := candidates[i]
			cost := len(c) + 4 // -i flag + path + separator slack
			if bytesUsed+cost > maxArgsBytes && bytesUsed > 0 {
				break
			}
			args = append(args, "-i", c)
			bytesUsed += cost
		}
		args = append(args, pakPath)
		if err := exec.Command(repakPath, args...).Run(); err != nil {
			return nil, fmt.Errorf("repak unpack batch: %w", err)
		}
	}

	set := map[string]struct{}{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".uasset") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, m := range crossPakPattern.FindAllSubmatch(raw, -1) {
			ref := string(m[1])
			if ref == "" {
				continue
			}
			set[ref] = struct{}{}
		}
		return nil
	})

	out := make([]string, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	return out, nil
}

// friendlyLabel picks the most human-readable identifier we have for a
// pak: prefer the display name from a previous scan if one exists, else
// fall back to the codename. Used in progress log lines so the user
// sees "scanning WCML South - London Euston to Milton Keynes…" instead
// of just "scanning WCMLSouth…".
func friendlyLabel(codename string, p *Pak) string {
	if p != nil && p.DisplayName != "" {
		return p.DisplayName
	}
	return codename
}

// entryKindLabel summarises a freshly-scanned pak in one word for the
// completion log line: "route" if it ships its own RouteDefinition,
// otherwise "addon".
func entryKindLabel(p *Pak) string {
	if p == nil {
		return "?"
	}
	if p.HasRouteDef {
		return "route"
	}
	return "addon"
}

// listTimetableShapeAssets returns the pak entries that COULD carry a
// cross-pak RouteDefinition reference: anything under /Scenarios/,
// /Timetables/, or /Training/ ending in .uasset. Path matching is
// case-insensitive — observed casing varies pak-to-pak ("Routedefinition"
// vs "RouteDefinition", "Scenarios" vs "scenarios").
func listTimetableShapeAssets(pakPath, repakPath string) ([]string, error) {
	out, err := exec.Command(repakPath, "list", pakPath).Output()
	if err != nil {
		return nil, fmt.Errorf("repak list: %w", err)
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
		if strings.Contains(lower, "/scenarios/") ||
			strings.Contains(lower, "/timetables/") ||
			strings.Contains(lower, "/timetable/") ||
			strings.Contains(lower, "/training/") {
			candidates = append(candidates, line)
		}
	}
	return candidates, sc.Err()
}
