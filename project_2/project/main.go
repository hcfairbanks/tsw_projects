package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"tsw6-timetable/internal/extractor"
	"tsw6-timetable/internal/output"
	"tsw6-timetable/internal/pak"
)

var (
	tswPath       string
	uassetGUIPath string
	unrealPakPath string
	repakPath     string
	aesKey        string
	outputFormat  string
	outputFile    string
	routeFilter   string
	serviceFilter string
	skipRibbons   bool
	verbose       bool
	debug         bool

	// index command flags
	indexPath  string
	indexForce bool
	indexOnly  string
)

func main() {
	root := &cobra.Command{
		Use:   "tsw6-timetable",
		Short: "Extract timetable data from Train Sim World 6 game files",
		Long: `tsw6-timetable extracts service mode timetable data from TSW6's
pak/uasset files and outputs structured JSON or CSV.

Prerequisites:
  - TSW6 installed via Steam
  - UAssetGUI.exe  https://github.com/atenfyr/UAssetGUI/releases
  - UnrealPak.exe  ships with the TSW Editor or any UE4.27 install

Quick start:
  tsw6-timetable list    --tsw "C:\Steam\steamapps\common\Train Sim World 6"
  tsw6-timetable extract --tsw "C:\Steam\steamapps\common\Train Sim World 6" --format csv --out timetables.csv
`,
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List available routes in the TSW6 install",
		RunE:  runList,
	}
	listCmd.Flags().StringVar(&tswPath, "tsw", "", "Path to TSW6 Steam installation directory (required)")
	_ = listCmd.MarkFlagRequired("tsw")

	extractCmd := &cobra.Command{
		Use:   "extract",
		Short: "Extract timetable data from TSW6 game files",
		RunE:  runExtract,
	}
	extractCmd.Flags().StringVar(&tswPath, "tsw", "", "Path to TSW6 Steam installation directory (required)")
	extractCmd.Flags().StringVar(&uassetGUIPath, "uassetgui", "", "Path to UAssetGUI.exe (auto-detected if omitted)")
	extractCmd.Flags().StringVar(&repakPath, "repak", "", "Path to repak.exe (auto-detected if omitted; preferred over UnrealPak)")
	extractCmd.Flags().StringVar(&unrealPakPath, "unrealpak", "", "Path to UnrealPak.exe (fallback if repak not found)")
	extractCmd.Flags().StringVar(&aesKey, "aeskey", "", "AES-256 decryption key for encrypted paks (hex, without 0x prefix)")
	extractCmd.Flags().StringVar(&outputFormat, "format", "json", "Output format: json, csv, or package (per-service JSON files in a zip/folder)")
	extractCmd.Flags().StringVar(&outputFile, "out", "", "Output file/folder path (default: stdout for json/csv; required for package)")
	extractCmd.Flags().StringVar(&routeFilter, "route", "", "Only extract routes whose pak name contains this substring")
	extractCmd.Flags().StringVar(&serviceFilter, "service", "", "Only keep services whose Name or FriendlyName contains this substring (case-insensitive)")
	extractCmd.Flags().BoolVar(&skipRibbons, "skip-ribbons", false, "Skip the tile-umap ribbon scan (fast iteration; disables per-stop lat/lng)")
	extractCmd.Flags().BoolVar(&verbose, "verbose", false, "Print verbose progress to stderr")
	extractCmd.Flags().BoolVar(&debug, "debug", false, "Keep temp dir and show raw JSON paths (for troubleshooting)")
	_ = extractCmd.MarkFlagRequired("tsw")

	indexCmd := &cobra.Command{
		Use:   "index",
		Short: "Build/refresh the global ribbon index across every installed pak",
		Long: `Walks every TSW6 pak (route DLCs, gameplay supplements, content packs)
one-at-a-time, scans each for NetworkRibbon records, and writes a merged
index. Re-running picks up only paks whose mtime/size changed since the
last run — so adding a DLC patch or new route only re-scans that one pak.

Resumable: the index is saved after each pak completes.

The index is consumed by 'tsw6-timetable extract' to fill in lat/lng for
ribbons that live in shared/cross-route paks.`,
		RunE: runIndex,
	}
	indexCmd.Flags().StringVar(&tswPath, "tsw", "", "Path to TSW6 Steam installation directory (required)")
	indexCmd.Flags().StringVar(&uassetGUIPath, "uassetgui", "", "Path to UAssetGUI.exe (auto-detected if omitted)")
	indexCmd.Flags().StringVar(&repakPath, "repak", "", "Path to repak.exe (auto-detected if omitted; preferred over UnrealPak)")
	indexCmd.Flags().StringVar(&unrealPakPath, "unrealpak", "", "Path to UnrealPak.exe (fallback if repak not found)")
	indexCmd.Flags().StringVar(&aesKey, "aeskey", "", "AES-256 decryption key for encrypted paks (hex, without 0x prefix)")
	indexCmd.Flags().StringVar(&indexPath, "index", "", "Path to the index file (default: <exe-dir>/ribbon-index.json)")
	indexCmd.Flags().BoolVar(&indexForce, "force", false, "Re-scan paks even when their mtime/size match the cached entry")
	indexCmd.Flags().StringVar(&indexOnly, "only", "", "Only scan paks whose path contains this substring (case-insensitive; testing)")
	indexCmd.Flags().BoolVar(&verbose, "verbose", false, "Print verbose progress to stderr")
	indexCmd.Flags().BoolVar(&debug, "debug", false, "Keep temp dir and show raw JSON paths (for troubleshooting)")
	_ = indexCmd.MarkFlagRequired("tsw")

	root.AddCommand(listCmd, extractCmd, indexCmd)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func runList(_ *cobra.Command, _ []string) error {
	routes, err := pak.DiscoverRoutes(tswPath)
	if err != nil {
		return fmt.Errorf("discovering routes: %w", err)
	}
	if len(routes) == 0 {
		fmt.Fprintln(os.Stderr, "No routes found — check your --tsw path.")
		return nil
	}
	fmt.Printf("Found %d route(s)\n\n", len(routes))
	fmt.Printf("  %-44s  %s\n", "ROUTE NAME", "PAK FILE")
	fmt.Printf("  %-44s  %s\n", "──────────", "────────")
	for _, r := range routes {
		fmt.Printf("  %-44s  %s\n", r.Name, r.PakPath)
	}
	return nil
}

func runExtract(_ *cobra.Command, _ []string) error {
	cfg := extractor.Config{
		TSWPath:        tswPath,
		UAssetGUIPath:  uassetGUIPath,
		UnrealPakPath:  unrealPakPath,
		RepakPath:      repakPath,
		AESKey:         aesKey,
		RouteFilter:    routeFilter,
		ServiceFilter:  serviceFilter,
		SkipRibbonScan: skipRibbons,
		Verbose:        verbose,
		Debug:          debug,
	}
	if err := cfg.AutoDetect(); err != nil {
		return err
	}

	ext := extractor.New(cfg)
	timetables, err := ext.Extract()
	if err != nil {
		return fmt.Errorf("extraction failed: %w", err)
	}
	if len(timetables) == 0 {
		fmt.Fprintln(os.Stderr, "No timetable data found.")
		return nil
	}

	switch outputFormat {
	case "package":
		if outputFile == "" {
			return fmt.Errorf("--out is required for --format package (directory or .zip path)")
		}
		n, err := output.WritePackageFiltered(outputFile, timetables, serviceFilter)
		if err != nil {
			return fmt.Errorf("writing package: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Wrote %d service file(s) to %s\n", n, outputFile)
		return nil
	case "json", "csv":
		w := os.Stdout
		if outputFile != "" {
			f, err := os.Create(outputFile)
			if err != nil {
				return fmt.Errorf("creating output file: %w", err)
			}
			defer f.Close()
			w = f
		}
		if outputFormat == "json" {
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			return enc.Encode(timetables)
		}
		return output.WriteCSV(w, timetables)
	default:
		return fmt.Errorf("unknown format %q — use json, csv, or package", outputFormat)
	}
}

func runIndex(_ *cobra.Command, _ []string) error {
	cfg := extractor.Config{
		TSWPath:       tswPath,
		UAssetGUIPath: uassetGUIPath,
		UnrealPakPath: unrealPakPath,
		RepakPath:     repakPath,
		AESKey:        aesKey,
		Verbose:       verbose,
		Debug:         debug,
	}
	if err := cfg.AutoDetect(); err != nil {
		return err
	}
	ext := extractor.New(cfg)
	return ext.BuildRibbonIndex(extractor.BuildIndexOptions{
		IndexPath: indexPath,
		Force:     indexForce,
		OnlyMatch: indexOnly,
	})
}