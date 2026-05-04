// umap.go — direct binary parser for UE 4.27 .uasset/.uexp pairs (umap files).
//
// Why this exists: UAssetGUI OOMs on big TS_*.umap tiles because its JSON
// serializer builds one giant StringBuilder. We need ribbon-grouping data
// from those tiles and have no other way to get it. This reader does only
// what we need — read the package summary, NameMap, and ExportMap so the
// existing `reader` (FPropertyTag walker) can be pointed at any export's
// property stream in the .uexp body.
//
// Field-layout reference cribbed from Vilsol/ue4pak's parser_asset.go and
// the open file-format docs at https://atenfyr.github.io/UAssetAPI/. We
// only decode the few summary fields we need (NameOffset/Count, ExportOffset/
// Count, TotalHeaderSize) and skip the rest by absolute seek.
package uasset

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// uassetMagic — the four bytes every .uasset starts with (little-endian).
const uassetMagic uint32 = 0x9E2A83C1

// Umap is a minimally-parsed .uasset/.uexp pair: enough metadata to point
// the property walker at any export's property stream.
type Umap struct {
	Names   []string      // NameMap (FName lookup table)
	Exports []ExportEntry // ExportMap

	uexp       []byte // body of the .uexp file (truncated to drop trailing magic)
	headerSize int    // size of the .uasset file (== summary's TotalHeaderSize)
}

// ExportEntry is the subset of FObjectExport we need to seek to a property
// stream and identify what kind of object we're looking at.
//
// OuterIndex is an FPackageIndex (UE convention): positive = an export at
// (idx-1), negative = an import, 0 = top-level (this object IS the package).
// We track it so we can walk Component → owning Actor for transform
// composition.
type ExportEntry struct {
	ObjectName   string
	ClassIndex   int32
	OuterIndex   int32 // see comment above
	SerialOffset int64
	SerialSize   int64
}

// ReadUmap opens a .uasset file (and its companion .uexp) and parses just
// enough of the summary + NameMap + ExportMap to drive a property walker.
// The .uexp body is held in memory; for the biggest TS tile this is ~138 MB
// and matters less than UAssetGUI's full export tree.
func ReadUmap(uassetPath string) (*Umap, error) {
	uassetData, err := os.ReadFile(uassetPath)
	if err != nil {
		return nil, fmt.Errorf("read .uasset: %w", err)
	}
	uexpPath := strings.TrimSuffix(uassetPath, filepath.Ext(uassetPath)) + ".uexp"
	uexpData, err := os.ReadFile(uexpPath)
	if err != nil {
		return nil, fmt.Errorf("read .uexp: %w", err)
	}
	// .uexp ends with the same 4-byte magic as the asset header — strip it
	// so absolute offsets land at the right byte even at the very end.
	if len(uexpData) >= 4 &&
		binary.LittleEndian.Uint32(uexpData[len(uexpData)-4:]) == uassetMagic {
		uexpData = uexpData[:len(uexpData)-4]
	}

	// We use the existing `reader` for binary primitives. NameMap isn't
	// known yet — set it after we read it.
	r := &reader{d: uassetData}

	if uint32(r.i32()) != uassetMagic {
		return nil, fmt.Errorf("%s: bad magic", uassetPath)
	}
	legacy := r.i32() // LegacyFileVersion (negative)
	if legacy != -4 {
		// All UE4 versions use legacy != -4 and include a LegacyUE3Version int32.
		r.skip(4) // LegacyUE3Version
	}
	r.skip(4) // FileVersionUE4
	r.skip(4) // FileVersionLicenseeUE4

	// CustomVersionContainer: int32 count then count * (FGuid + int32) = 20 bytes.
	cvCount := int(r.i32())
	r.skip(cvCount * 20)

	totalHeaderSize := int(r.i32())
	_ = r.fstr() // FolderName
	r.skip(4)    // PackageFlags

	nameCount := int(r.i32())
	nameOffset := int(r.i32())
	r.skip(4) // GatherableTextDataCount
	r.skip(4) // GatherableTextDataOffset
	exportCount := int(r.i32())
	exportOffset := int(r.i32())
	// We don't need anything past here — fields like ImportOffset, DependsOffset,
	// etc. are not consulted because we only read a known subset of exports.

	// --- NameMap ---
	r.seek(nameOffset)
	names := make([]string, nameCount)
	for i := range names {
		names[i] = r.fstr()
		r.skip(4) // NonCasePreservingHash (uint16) + CasePreservingHash (uint16)
	}

	// Make fname() work for ObjectName lookups during ExportMap parsing.
	r.nm = names

	// --- ExportMap ---
	// FObjectExport in UE 4.27 is a fixed-size record (modulo the FName
	// pair which is two int32s). Total = 4*5 (indices) + 8 (FName) + 4 (Save) +
	// 8 (SerialSize) + 8 (SerialOffset) + 4*3 (3 bools) + 16 (FGuid) + 4 (flags) +
	// 4*2 (2 bools) + 4 (FirstExportDependency) + 4*4 (4 dependency counts) = 100 bytes.
	r.seek(exportOffset)
	exports := make([]ExportEntry, exportCount)
	for i := range exports {
		var e ExportEntry
		e.ClassIndex = r.i32()
		r.skip(4) // SuperIndex
		r.skip(4) // TemplateIndex
		e.OuterIndex = r.i32()
		e.ObjectName = r.fname()
		r.skip(4) // Save / ObjectFlags
		e.SerialSize = r.i64()
		e.SerialOffset = r.i64()
		// Skip the rest of FObjectExport: 12 (3 bools) + 16 (PackageGuid) + 4
		// (PackageFlags) + 8 (2 bools) + 4 (FirstExportDependency) + 16 (4 dep
		// counts) = 60 bytes.
		r.skip(60)
		exports[i] = e
	}

	return &Umap{
		Names:      names,
		Exports:    exports,
		uexp:       uexpData,
		headerSize: totalHeaderSize,
	}, nil
}

// PropertyReader returns a `reader` positioned at the start of an export's
// property stream, with the NameMap installed and the slice bounded to the
// export's serial body so out-of-range reads are caught early.
//
// Returns nil if the export's data isn't located in .uexp (would mean it's
// in the .uasset itself — never happens for the exports we care about, but
// callers should still nil-check).
func (u *Umap) PropertyReader(e ExportEntry) *reader {
	off := int(e.SerialOffset) - u.headerSize
	if off < 0 || off >= len(u.uexp) {
		return nil
	}
	end := off + int(e.SerialSize)
	if end > len(u.uexp) {
		end = len(u.uexp)
	}
	return &reader{d: u.uexp[off:end], nm: u.Names}
}
