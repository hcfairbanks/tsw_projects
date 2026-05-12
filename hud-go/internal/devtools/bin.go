// Package devtools provides small helpers for the cmd/ dev tools that
// shell out to repak.exe / UAssetGUI.exe. The main app uses its own
// fully-featured lookup in internal/extractor; this is the lower-touch
// version for one-shot scripts.
//
// Lookup order matches the main app: resources/<name>.exe next to the
// current binary first, then the legacy spot directly next to the
// binary, then "./resources/<name>.exe" relative to cwd (for `go run`),
// then "./<name>.exe", then PATH.
package devtools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

// FindBin locates a bundled .exe by name. Returns the resolved path or
// an error explaining where it looked. Pass names without the .exe
// suffix on PATH (e.g. "repak"), with the .exe suffix for the
// resources/ / cwd / exeDir checks (windows is case-insensitive).
func FindBin(name string) (string, error) {
	withExe := name + ".exe"
	candidates := []string{}
	if d := exeDir(); d != "" {
		candidates = append(candidates,
			filepath.Join(d, "resources", withExe),
			filepath.Join(d, withExe))
	}
	candidates = append(candidates,
		filepath.Join("resources", withExe),
		filepath.Join(".", withExe))
	for _, p := range candidates {
		if fileExists(p) {
			abs, err := filepath.Abs(p)
			if err == nil {
				return abs, nil
			}
			return p, nil
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("%s not found: looked in %v and PATH", withExe, candidates)
}

// MustFindBin is FindBin but exits the process with a clear error on
// failure. Use from cmd/<tool>/main.go init paths where there's nothing
// to recover.
func MustFindBin(name string) string {
	p, err := FindBin(name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	return p
}
