package pak

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"hud-go/internal/pak/uasset"
)

// PakRVDs extracts every `RVD_*.uasset` from a pak, runs the in-process
// parser on each, and returns the parsed slice with each RVD's
// `AssetPath` set to the canonical reference key the timetable's
// CompiledRVMap uses to look it up.
//
// Used by the catalog scan to persist per-pak RVDs once at
// scan-time, so per-route extraction can skip re-walking every
// installed pak for trains.
//
// Cost: one `repak unpack` (batched if there are lots of files —
// Windows argv cap is ~32 KB) plus the in-process parse. The third
// parameter is unused but kept for caller compatibility (was previously
// the UAssetGUI path before we switched to the in-process parser).
//
// scratchDir must already exist; callers are responsible for any
// cleanup. The implementation chooses a private subdir keyed by pak
// basename so concurrent calls don't collide. The subdir is removed
// before return regardless of success — callers that need adjacent
// assets (thumbnails, manufacturer logos) should pass `thumbsOutDir`
// so we render them before cleanup.
//
// Convenience wrapper around PakRVDsWithThumbnails for legacy callers
// that don't render thumbnails.
func PakRVDs(pakPath, repakPath, _ /* uassetGUIPath, kept for caller compat */, scratchDir string) ([]*uasset.RVD, error) {
	return PakRVDsWithThumbnails(pakPath, repakPath, scratchDir, "")
}

// PakRVDsWithThumbnails is PakRVDs plus thumbnail rendering. When
// `thumbsOutDir` is non-empty, every drivable RVD whose ThumbnailTexture
// resolves to a .uasset in the scratch tree gets rendered to PNG under
// `thumbsOutDir/<sanitised friendly name>.png` before the scratch is
// torn down. The catalog scan uses this so the Train Classes page can
// show thumbnails without keeping the unpacked tree around.
func PakRVDsWithThumbnails(pakPath, repakPath, scratchDir, thumbsOutDir string) ([]*uasset.RVD, error) {
	if pakPath == "" || repakPath == "" {
		return nil, fmt.Errorf("PakRVDs: empty pakPath / repakPath")
	}
	if scratchDir == "" {
		return nil, fmt.Errorf("PakRVDs: empty scratchDir")
	}

	// 1. Walk the pak's central directory for RVD candidates. Pattern:
	//    basename starts with "RVD_" and ends in ".uasset".
	out, err := exec.Command(repakPath, "list", pakPath).Output()
	if err != nil {
		return nil, fmt.Errorf("repak list %s: %w", pakPath, err)
	}
	var entries []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		base := filepath.Base(line)
		if strings.HasPrefix(base, "RVD_") && strings.HasSuffix(base, ".uasset") {
			entries = append(entries, line)
			// Also pick up the matching .uexp — both files are needed
			// for the in-process parser to read the asset's exports.
			entries = append(entries, strings.TrimSuffix(line, ".uasset")+".uexp")
			continue
		}
		// When the caller requested thumbnail rendering, also unpack any
		// .uasset under "FrontEnd" / "FrontEndAssets" directories — these
		// host the per-class thumbnail textures referenced by each RVD's
		// ThumbnailTexture SoftObjectProperty. TSW's path conventions
		// vary across DLC vendors (some use "FrontEndAssets", others
		// "FrontEnd"); both patterns are picked up here.
		if thumbsOutDir != "" && strings.HasSuffix(line, ".uasset") &&
			(strings.Contains(line, "/FrontEndAssets/") || strings.Contains(line, "/FrontEnd/")) {
			entries = append(entries, line)
			entries = append(entries, strings.TrimSuffix(line, ".uasset")+".uexp")
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("repak list %s: %w", pakPath, err)
	}
	if len(entries) == 0 {
		return nil, nil
	}

	pakStem := strings.TrimSuffix(filepath.Base(pakPath), filepath.Ext(pakPath))
	root := filepath.Join(scratchDir, "rvds_"+pakStem)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir scratch %s: %w", root, err)
	}
	defer os.RemoveAll(root)

	// 2. Batch the repak unpack so we don't blow Windows' ~32 KB argv
	//    cap. Each `-i path` adds the full pak-internal path; ~4 KB
	//    per batch keeps comfortable headroom.
	const maxArgsBytes = 4096
	for i := 0; i < len(entries); {
		args := []string{"unpack", "-f", "-o", root}
		bytesUsed := 0
		for ; i < len(entries); i++ {
			c := entries[i]
			cost := len(c) + 4
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

	// 3. Walk the unpacked tree, run UAssetGUI on each RVD asset,
	//    parse the JSON, stamp the canonical asset_path, collect.
	var result []*uasset.RVD
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if !strings.HasPrefix(base, "RVD_") || !strings.HasSuffix(base, ".uasset") {
			return nil
		}
		rvd, err := uasset.ParseCookedRVD(path)
		if err != nil {
			return nil
		}
		rvd.AssetPath = CanonicalRVDPath(path)
		result = append(result, rvd)
		return nil
	})

	// Render thumbnails before the scratch tree gets deleted by the
	// deferred RemoveAll above. Best-effort: a missing or unreadable
	// thumbnail doesn't fail the catalog scan.
	if thumbsOutDir != "" && len(result) > 0 {
		_ = renderRVDThumbnails(root, thumbsOutDir, result)
	}
	return result, nil
}

// renderRVDThumbnails walks the unpacked tree for each RVD's
// ThumbnailTexture and writes the texture to PNG under `outDir` with a
// deterministic filename derived from the RVD's FriendlyName (the class
// label). Idempotent — re-running over an existing dir just overwrites.
func renderRVDThumbnails(scratchRoot, outDir string, rvds []*uasset.RVD) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	// Index every .uasset under the scratch tree by its filename stem so
	// we can look up each RVD's ThumbnailTexture cheaply.
	index := map[string]string{}
	_ = filepath.WalkDir(scratchRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".uasset") {
			return nil
		}
		index[strings.TrimSuffix(filepath.Base(path), ".uasset")] = path
		return nil
	})
	seen := map[string]bool{}
	for _, rvd := range rvds {
		if rvd == nil || !rvd.Drivable || rvd.ThumbnailAssetRef == "" || rvd.FriendlyName == "" {
			continue
		}
		ref := rvd.ThumbnailAssetRef
		if i := strings.LastIndex(ref, "."); i >= 0 {
			ref = ref[:i]
		}
		stem := ref
		if i := strings.LastIndex(stem, "/"); i >= 0 {
			stem = stem[i+1:]
		}
		assetPath, ok := index[stem]
		if !ok {
			continue
		}
		fname := SanitiseThumbnailName(rvd.FriendlyName) + ".png"
		if seen[fname] {
			continue
		}
		seen[fname] = true
		_, _ = uasset.ExtractTexture2DPNG(assetPath, filepath.Join(outDir, fname))
	}
	return nil
}

// SanitiseThumbnailName turns a class display name into a filesystem-
// safe stem. Public so both the catalog scan (writes the PNG) and the
// importer (records the relative URL on train_classes.thumbnail_path)
// agree on the naming scheme.
func SanitiseThumbnailName(name string) string {
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

// CanonicalRVDPath turns an on-disk RVD `.uasset` path into the short
// reference form used inside CompiledRVMap entries
// (e.g. "/LIRREX_M7/Data/RailVehicleDefinition/RVD_LIRREX_M7-A").
//
// Strips the `<scratch>/.../Plugins/DLC/` prefix, drops the
// `/Content/` segment (the asset path uses the plugin mount path
// without /Content/), and drops the .uasset extension.
//
// Exported because both the in-extractor RVD scan and the catalog-time
// scan need to produce identical keys so the lookup at write time
// works.
func CanonicalRVDPath(diskPath string) string {
	p := filepath.ToSlash(diskPath)
	const marker = "/Plugins/DLC/"
	if idx := strings.Index(p, marker); idx >= 0 {
		p = p[idx+len(marker)-1:] // keep leading "/"
	}
	p = strings.Replace(p, "/Content/", "/", 1)
	p = strings.TrimSuffix(p, ".uasset")
	return p
}
