package extractor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
