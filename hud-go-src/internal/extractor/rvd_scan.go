package extractor

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"hud-go/internal/pak"
	"hud-go/internal/pak/uasset"
)

// ScanAllRVDs walks every installed TSW6 pak, extracts just the
// RailVehicleDefinition (RVD) assets from each, converts them with UAssetGUI,
// and returns a single keyed map of all RVDs discovered across all paks.
//
// This gives us full RailVehicleClass resolution coverage across routes that
// reuse vehicles from other DLC paks (Acela in Boston Sprinter, CSX freight
// cars on multiple routes, etc.).
//
// Uses a temp working dir that's cleaned up after each pak, so the peak disk
// usage is bounded by the largest single pak's RVD set (usually < 50 MB).
func (e *Extractor) ScanAllRVDs(workDir string) (map[string]*uasset.RVD, error) {
	routes, err := pak.DiscoverRoutes(e.cfg.TSWPath)
	if err != nil {
		return nil, err
	}

	// Build a unique list of pak files to inspect (DiscoverRoutes may return
	// several entries pointing at the same pak in edge cases).
	seen := map[string]bool{}
	paks := routes[:0]
	for _, r := range routes {
		if seen[r.PakPath] {
			continue
		}
		seen[r.PakPath] = true
		paks = append(paks, r)
	}

	out := make(map[string]*uasset.RVD)
	for _, r := range paks {
		e.logf("[rvd-scan] %s\n", filepath.Base(r.PakPath))
		rvdMap, err := e.scanOnePakRVDs(r.PakPath, workDir)
		if err != nil {
			if e.cfg.Debug {
				fmt.Fprintf(os.Stderr, "[rvd-scan] %s: %v\n", filepath.Base(r.PakPath), err)
			}
			continue
		}
		for k, v := range rvdMap {
			if _, ok := out[k]; !ok {
				out[k] = v
			}
		}
	}
	return out, nil
}

// scanOnePakRVDs extracts the RVD assets from a single pak into a temporary
// directory, parses them, deletes the scratch dir, and returns the RVD map.
func (e *Extractor) scanOnePakRVDs(pakPath, workRoot string) (map[string]*uasset.RVD, error) {
	// 1. List the pak contents, keeping only RVD .uasset + .uexp lines.
	lines, err := e.repakListRVDs(pakPath)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, nil
	}

	// 2. Extract only those files to a scratch dir.
	scratch, err := os.MkdirTemp(workRoot, "rvd-*")
	if err != nil {
		return nil, err
	}
	defer func() {
		if !e.cfg.Debug {
			os.RemoveAll(scratch)
		}
	}()

	args := []string{}
	if e.cfg.AESKey != "" {
		args = append(args, "--aes-key", "0x"+e.cfg.AESKey)
	}
	args = append(args, "unpack", "--output", scratch)
	for _, p := range lines {
		args = append(args, "-i", p)
	}
	args = append(args, pakPath)

	cmd := exec.Command(e.cfg.RepakPath, args...)
	cmd.Stdout = e.logWriter()
	cmd.Stderr = e.logWriter()
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("repak unpack: %w", err)
	}

	// 3. Walk the scratch dir, convert each RVD .uasset to JSON, parse.
	out := map[string]*uasset.RVD{}
	_ = filepath.WalkDir(scratch, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := filepath.Base(path)
		if !pak.IsRVDAsset(name) {
			return nil
		}
		rvd, err := uasset.ParseCookedRVD(path)
		if err != nil {
			return nil
		}
		key := canonicalRVDPath(path)
		out[key] = rvd
		return nil
	})
	return out, nil
}

// repakListRVDs runs `repak list` and returns the subset of entries that are
// RVD assets (both the .uasset header and .uexp body — we need both to convert).
func (e *Extractor) repakListRVDs(pakPath string) ([]string, error) {
	args := []string{}
	if e.cfg.AESKey != "" {
		args = append(args, "--aes-key", "0x"+e.cfg.AESKey)
	}
	args = append(args, "list", pakPath)
	cmd := exec.Command(e.cfg.RepakPath, args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = e.logWriter()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var rvds []string
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		base := filepath.Base(line)
		// Accept both RVD_<X> (prefix) and <X>_RVD (suffix) naming
		// conventions; see pak.IsRVDAsset for full rationale. Here we
		// additionally accept the matching .uexp / .ubulk sidecars
		// regardless of suffix form so repak unpacks the full RVD body.
		if !(strings.HasSuffix(base, ".uasset") || strings.HasSuffix(base, ".uexp") || strings.HasSuffix(base, ".ubulk")) {
			continue
		}
		stem := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(base, ".uasset"), ".uexp"), ".ubulk")
		if !(strings.HasPrefix(stem, "RVD_") || strings.HasSuffix(stem, "_RVD")) {
			continue
		}
		rvds = append(rvds, line)
	}
	_ = cmd.Wait()
	return rvds, nil
}
