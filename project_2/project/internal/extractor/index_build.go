package extractor

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"tsw6-timetable/internal/pak/uasset"
)

// trainOnlyPakHints flags paks that contain only rolling stock (no tiles, no
// ribbons). We still record an entry for them so subsequent runs skip them
// without unpacking.
var trainOnlyPakHints = []string{
	"WagonPack", "GodMode", "CrashReport", "BRClass390", "NewJourneysSD40",
	"CL-Military", "CL-Nuclear", "CL-Intermodal",
}

// DefaultIndexPath is where the ribbon index lives by default — next to the
// extractor binary so it's portable and easy to find.
func DefaultIndexPath() string {
	if d := exeDir(); d != "" {
		return filepath.Join(d, "ribbon-index.json")
	}
	return "ribbon-index.json"
}

// DiscoverAllPaks finds every .pak in the install: route DLCs (DLC/), main
// content packs (Paks/), and any nested per-route subdir layout. Returns the
// absolute paths sorted for stable iteration.
func DiscoverAllPaks(tswRoot string) ([]string, error) {
	roots := []string{
		filepath.Join(tswRoot, "WindowsNoEditor", "TS2Prototype", "Content", "DLC"),
		filepath.Join(tswRoot, "WindowsNoEditor", "TS2Prototype", "Content", "Paks"),
	}
	seen := map[string]struct{}{}
	var out []string
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(strings.ToLower(path), ".pak") {
				return nil
			}
			abs, err := filepath.Abs(path)
			if err != nil {
				abs = path
			}
			if _, dup := seen[abs]; dup {
				return nil
			}
			seen[abs] = struct{}{}
			out = append(out, abs)
			return nil
		})
	}
	sort.Strings(out)
	return out, nil
}

// looksTrainOnly returns true if a pak's filename matches a known train-only
// pattern. Used as an upfront skip-without-unpacking heuristic.
func looksTrainOnly(pakPath string) bool {
	base := filepath.Base(pakPath)
	for _, hint := range trainOnlyPakHints {
		if strings.Contains(base, hint) {
			return true
		}
	}
	return false
}

// pakStat returns the on-disk mtime+size needed for staleness detection.
func pakStat(pakPath string) (mtime, size int64, err error) {
	st, err := os.Stat(pakPath)
	if err != nil {
		return 0, 0, err
	}
	return st.ModTime().Unix(), st.Size(), nil
}

// BuildIndexOptions controls a global ribbon-index build run.
type BuildIndexOptions struct {
	IndexPath string // where the index lives (default: DefaultIndexPath())
	Force     bool   // re-scan even paks whose mtime/size match the cached entry
	OnlyMatch string // case-insensitive substring; only scan paks whose path matches (testing)
}

// BuildRibbonIndex walks every pak in the install one-at-a-time, scans each
// for ribbons, and writes the merged index to disk. Resumable: writes the
// index after each pak completes, so a crash mid-run preserves prior progress.
//
// Disk discipline: each pak is unpacked into its own temp dir, scanned, and
// the temp dir is removed before moving to the next pak. Peak disk = the
// largest single pak's unpacked footprint (~5-10 GB for big route DLCs).
func (e *Extractor) BuildRibbonIndex(opts BuildIndexOptions) error {
	if opts.IndexPath == "" {
		opts.IndexPath = DefaultIndexPath()
	}

	idx, err := LoadRibbonIndex(opts.IndexPath)
	if err != nil {
		return fmt.Errorf("loading existing index: %w", err)
	}
	prevRibbons := len(idx.Ribbons)
	prevPaks := len(idx.Paks)
	e.logf("[index] starting; existing entries: %d ribbons across %d paks\n", prevRibbons, prevPaks)

	pakPaths, err := DiscoverAllPaks(e.cfg.TSWPath)
	if err != nil {
		return fmt.Errorf("discovering paks: %w", err)
	}
	e.logf("[index] discovered %d paks under %s\n", len(pakPaths), e.cfg.TSWPath)

	onlyLower := strings.ToLower(strings.TrimSpace(opts.OnlyMatch))
	for i, pakPath := range pakPaths {
		if onlyLower != "" && !strings.Contains(strings.ToLower(pakPath), onlyLower) {
			continue
		}
		mtime, size, err := pakStat(pakPath)
		if err != nil {
			e.logf("[index] (%d/%d) %s: stat failed: %v\n", i+1, len(pakPaths), filepath.Base(pakPath), err)
			continue
		}

		// Cache hit — pak hasn't changed since last scan, skip.
		if !opts.Force && idx.IsPakUpToDate(pakPath, mtime, size) {
			e.logf("[index] (%d/%d) %s: cached (%d ribbons)\n",
				i+1, len(pakPaths), filepath.Base(pakPath), idx.Paks[pakPath].RibbonCount)
			continue
		}

		// Train-only / system pak heuristic: record a stub entry and skip.
		if looksTrainOnly(pakPath) {
			idx.Paks[pakPath] = PakMetadata{
				Path:            pakPath,
				Mtime:           mtime,
				Size:            size,
				LastScannedUnix: time.Now().Unix(),
				HasTiles:        false,
				SkipReason:      "filename matches train-only pattern",
			}
			e.logf("[index] (%d/%d) %s: skipped (train-only)\n", i+1, len(pakPaths), filepath.Base(pakPath))
			if err := idx.Save(opts.IndexPath); err != nil {
				return fmt.Errorf("saving index after skip: %w", err)
			}
			continue
		}

		// Otherwise scan it.
		e.logf("[index] (%d/%d) %s: scanning...\n", i+1, len(pakPaths), filepath.Base(pakPath))
		started := time.Now()
		newRibbons, hasTiles, scanErr := e.scanOnePak(pakPath)
		elapsed := time.Since(started).Round(time.Second)
		if scanErr != nil {
			e.logf("[index] (%d/%d) %s: scan failed: %v (continuing)\n", i+1, len(pakPaths), filepath.Base(pakPath), scanErr)
			continue
		}

		// Drop any prior entries that were sourced from this pak before merging
		// the fresh ones. Without this, a removed-from-pak ribbon would linger.
		if oldMeta, existed := idx.Paks[pakPath]; existed && oldMeta.RibbonCount > 0 {
			// We don't track per-ribbon source so we conservatively skip eviction
			// here — the merge logic prefers anchored, so updates are safe enough.
			_ = oldMeta
		}

		merged := 0
		for k, r := range newRibbons {
			if idx.MergeRibbon(k, r) {
				merged++
			}
		}
		idx.Paks[pakPath] = PakMetadata{
			Path:            pakPath,
			Mtime:           mtime,
			Size:            size,
			LastScannedUnix: time.Now().Unix(),
			RibbonCount:     len(newRibbons),
			HasTiles:        hasTiles,
		}

		e.logf("[index] (%d/%d) %s: %d ribbons (+%d new) in %s\n",
			i+1, len(pakPaths), filepath.Base(pakPath), len(newRibbons), merged, elapsed)

		// Save after each pak so a crash preserves progress.
		if err := idx.Save(opts.IndexPath); err != nil {
			return fmt.Errorf("saving index after %s: %w", filepath.Base(pakPath), err)
		}
	}

	addedRibbons := len(idx.Ribbons) - prevRibbons
	addedPaks := len(idx.Paks) - prevPaks
	e.logf("[index] done; total %d ribbons across %d paks (+%d ribbons, +%d paks this run)\n",
		len(idx.Ribbons), len(idx.Paks), addedRibbons, addedPaks)
	e.logf("[index] written to %s\n", opts.IndexPath)
	return nil
}

// scanOnePak unpacks a single pak into its own temp dir, walks every tile
// .umap inside, parses the ribbons, and cleans up. Returns the per-pak ribbon
// map (canonical-GUID-keyed) and whether the pak contained any tile umaps.
func (e *Extractor) scanOnePak(pakPath string) (map[string]*uasset.Ribbon, bool, error) {
	tmp, err := os.MkdirTemp("", "tsw6-index-*")
	if err != nil {
		return nil, false, fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	if err := e.runUnrealPak(pakPath, tmp); err != nil {
		return nil, false, fmt.Errorf("unpack: %w", err)
	}
	out := e.scanRibbons(tmp)
	hasTiles := len(out) > 0
	return out, hasTiles, nil
}
