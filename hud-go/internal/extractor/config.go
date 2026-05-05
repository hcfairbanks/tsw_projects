package extractor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"hud-go/internal/pak/uasset"
)

// Config holds all paths and options for the extractor.
type Config struct {
	TSWPath       string
	UAssetGUIPath string
	UnrealPakPath string // UnrealPak.exe (fallback, no Oodle support)
	RepakPath     string // repak.exe (preferred, handles Oodle compression)
	AESKey        string // base64 AES-256 key (if paks are encrypted)
	RouteFilter   string
	// ServiceFilter, if set, keeps only services whose Name (backend ID) or
	// FriendlyName contains this substring (case-insensitive). Applied after
	// timetables are fully parsed; useful for iterating on a single service
	// without re-running the whole route pipeline.
	ServiceFilter string
	// SkipRibbonScan, if true, bypasses the tile-umap ribbon walk — the
	// slowest step in the pipeline. Lat/lng enrichment is disabled for the
	// run, but everything else (identity, trains, schedule, origin) works.
	SkipRibbonScan bool
	Verbose        bool
	Debug          bool

	// WorkDir, if set, is the parent directory under which the extractor
	// creates its temporary `tsw6-timetable-*` working dir for unpacked
	// paks and converted JSON. Empty = use the system temp dir.
	// Lets in-process callers point the extractor at a drive with enough
	// free space without having to mutate process-wide TMP/TEMP env vars.
	WorkDir string

	// KeepWorkDir, when true, suppresses the deferred RemoveAll of the
	// per-run temp dir (and therefore each route's `extractDir` inside it)
	// so callers can read the unpacked tile binaries after Extract returns.
	// Caller becomes responsible for cleanup. Each returned Timetable's
	// ExtractDir field points at its route-scoped subdirectory.
	KeepWorkDir bool

	// PackDisplayName, if set, overrides whatever the per-pak
	// RouteDefinition.DisplayName supplies — used for routes whose
	// asset ships an empty DisplayName (Great Western Express ships
	// nothing; the marketing name "Great Western Express" lives in
	// the pak's *_Gameplay.uplugin Description, which the catalog
	// scan reads via pak.PakDLCDisplayName). The handler passes the
	// catalog's already-resolved display name through here so the
	// route_<X>.json's `name` field matches the route list in the UI.
	PackDisplayName string

	// Logger, if set, receives every progress line the extractor would
	// otherwise write to stderr. Lets in-process callers (e.g. the hud-go
	// /extractor SSE handler) intercept progress without scraping a
	// subprocess's stderr. Lines may or may not include a trailing newline;
	// callbacks should tolerate both.
	//
	// When nil and Verbose is true, lines fall through to os.Stderr (CLI
	// behaviour, unchanged).
	Logger func(format string, args ...any)

	// PreloadedRVDs, if non-empty, bypasses the start-of-extraction
	// "Scanning for trains" pass (`ScanAllRVDs`). Keys are canonical
	// RVD asset paths (the form CompiledRVMap entries reference), values
	// are the parsed RVD records. Caller is expected to populate from
	// catalog.LoadAllRVDs (which the catalog scan persisted once at
	// scan time). Saves ~1–2 minutes per route extraction.
	PreloadedRVDs map[string]*uasset.RVD
}

// AutoDetect fills in tool paths if not provided.
func (c *Config) AutoDetect() error {
	if c.UAssetGUIPath == "" {
		p, err := findUAssetGUI()
		if err != nil {
			return fmt.Errorf("UAssetGUI not found — please provide --uassetgui: %w", err)
		}
		c.UAssetGUIPath = p
	}
	// repak is preferred over UnrealPak (handles Oodle compression)
	if c.RepakPath == "" {
		c.RepakPath, _ = findRepak()
	}
	if c.RepakPath == "" && c.UnrealPakPath == "" {
		p, err := findUnrealPak(c.TSWPath)
		if err != nil {
			return fmt.Errorf("no pak extractor found — download repak.exe from https://github.com/trumank/repak/releases and place it next to tsw6-timetable.exe: %w", err)
		}
		c.UnrealPakPath = p
	}
	return nil
}

func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

func findUAssetGUI() (string, error) {
	// 1. Bundled next to the binary
	if d := exeDir(); d != "" {
		if p := filepath.Join(d, "UAssetGUI.exe"); fileExists(p) {
			return p, nil
		}
	}
	// 2. On PATH
	if p, err := exec.LookPath("UAssetGUI"); err == nil {
		return p, nil
	}
	// 3. Common manual install locations
	candidates := []string{
		`C:\Tools\UAssetGUI\UAssetGUI.exe`,
		`C:\UAssetGUI\UAssetGUI.exe`,
		filepath.Join(os.Getenv("LOCALAPPDATA"), "UAssetGUI", "UAssetGUI.exe"),
		filepath.Join(os.Getenv("PROGRAMFILES"), "UAssetGUI", "UAssetGUI.exe"),
	}
	if runtime.GOOS == "windows" {
		for _, p := range candidates {
			if fileExists(p) {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("not found in PATH or common locations")
}

func findRepak() (string, error) {
	// 1. Bundled next to the binary
	if d := exeDir(); d != "" {
		if p := filepath.Join(d, "repak.exe"); fileExists(p) {
			return p, nil
		}
	}
	// 2. On PATH
	if p, err := exec.LookPath("repak"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("not found")
}

func findUnrealPak(tswRoot string) (string, error) {
	// 1. Bundled next to the binary
	if d := exeDir(); d != "" {
		if p := filepath.Join(d, "UnrealPakTool", "UnrealPakTool", "UnrealPak.exe"); fileExists(p) {
			return p, nil
		}
	}
	// 2. Bundled with TSW Editor
	bundled := filepath.Join(
		filepath.Dir(tswRoot),
		"Train Sim World 6 Editor",
		"Engine", "Binaries", "Win64", "UnrealPak.exe",
	)
	if fileExists(bundled) {
		return bundled, nil
	}
	// 3. UE4.27 default install
	ue4 := `C:\Program Files\Epic Games\UE_4.27\Engine\Binaries\Win64\UnrealPak.exe`
	if fileExists(ue4) {
		return ue4, nil
	}
	// 4. Anywhere on PATH
	if p, err := exec.LookPath("UnrealPak"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("not found; install repak, the TSW Editor, or UE4.27")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
