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

// PakRVDs extracts every `RVD_*.uasset` from a pak, runs UAssetGUI on
// each, and returns the parsed slice with each RVD's `AssetPath` set
// to the canonical reference key the timetable's CompiledRVMap uses
// to look it up.
//
// Used by the catalog scan to persist per-pak RVDs once at
// scan-time, so per-route extraction can skip re-walking every
// installed pak for trains.
//
// Cost: one `repak unpack` (batched if there are lots of files —
// Windows argv cap is ~32 KB) plus one UAssetGUI invocation per
// RVD. UAssetGUI cold-start dominates (~500 ms each); per pak that
// ships 5–30 RVDs this is ~5–20 s. Caller must cache.
//
// scratchDir must already exist; callers are responsible for any
// cleanup. The implementation chooses a private subdir keyed by pak
// basename so concurrent calls don't collide. The subdir is removed
// before return regardless of success.
func PakRVDs(pakPath, repakPath, _ /* uassetGUIPath, kept for caller compat */, scratchDir string) ([]*uasset.RVD, error) {
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
			// Also pick up the matching .uexp — repak unpack needs both
			// files together for UAssetGUI to round-trip the asset.
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
	return result, nil
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
