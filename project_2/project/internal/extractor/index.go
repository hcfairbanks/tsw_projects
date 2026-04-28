package extractor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"tsw6-timetable/internal/pak/uasset"
)

// IndexFormatVersion lets us evolve the on-disk schema without crashing on
// older index files. Bump when fields are removed or semantics change.
const IndexFormatVersion = 1

// PakMetadata records what we know about a single pak we've already scanned.
// On subsequent runs we compare Mtime+Size to the live file; if they match
// we trust the cached ribbons and skip re-scanning that pak.
type PakMetadata struct {
	Path            string `json:"path"`
	Mtime           int64  `json:"mtime_unix"`
	Size            int64  `json:"size"`
	LastScannedUnix int64  `json:"last_scanned_unix"`
	RibbonCount     int    `json:"ribbon_count"`
	HasTiles        bool   `json:"has_tiles"`
	SkipReason      string `json:"skip_reason,omitempty"`
}

// RibbonIndex is the global ribbon catalogue persisted across runs. Keyed by
// canonical (lowercase, no-separators) GUID so multi-pak duplicates merge.
// When the same ribbon appears in more than one pak (e.g. an anchored copy in
// a route's pak and a stub in a gameplay-pack), the anchored one wins.
type RibbonIndex struct {
	Version int                       `json:"version"`
	Paks    map[string]PakMetadata    `json:"paks"`    // pak path -> metadata
	Ribbons map[string]*uasset.Ribbon `json:"ribbons"` // canonical GUID -> ribbon
}

// NewRibbonIndex creates an empty index ready to receive entries.
func NewRibbonIndex() *RibbonIndex {
	return &RibbonIndex{
		Version: IndexFormatVersion,
		Paks:    map[string]PakMetadata{},
		Ribbons: map[string]*uasset.Ribbon{},
	}
}

// LoadRibbonIndex reads an index file from disk. Returns an empty index
// (no error) if the path doesn't exist yet — first-run case.
func LoadRibbonIndex(path string) (*RibbonIndex, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return NewRibbonIndex(), nil
		}
		return nil, fmt.Errorf("reading ribbon index: %w", err)
	}
	var idx RibbonIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("parsing ribbon index: %w", err)
	}
	if idx.Paks == nil {
		idx.Paks = map[string]PakMetadata{}
	}
	if idx.Ribbons == nil {
		idx.Ribbons = map[string]*uasset.Ribbon{}
	}
	if idx.Version == 0 {
		idx.Version = IndexFormatVersion
	}
	return &idx, nil
}

// Save writes the index to disk, atomically (write to .tmp, rename). Indented
// JSON keeps the file inspectable; gzipping is overkill for the volume.
func (idx *RibbonIndex) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(idx); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// IsPakUpToDate returns true if the on-disk pak matches what's recorded in the
// index. Mismatch means the pak got patched (or replaced) and needs re-scanning.
func (idx *RibbonIndex) IsPakUpToDate(pakPath string, mtime, size int64) bool {
	meta, ok := idx.Paks[pakPath]
	if !ok {
		return false
	}
	return meta.Mtime == mtime && meta.Size == size
}

// MergeRibbon inserts or upgrades a ribbon entry. Anchored entries always win
// over unanchored; among entries of the same anchor status, first one wins.
// Returns true if the entry was newly added (not a merge of an existing key).
func (idx *RibbonIndex) MergeRibbon(key string, r *uasset.Ribbon) bool {
	existing, ok := idx.Ribbons[key]
	if !ok {
		idx.Ribbons[key] = r
		return true
	}
	if r.HasAnchor && !existing.HasAnchor {
		idx.Ribbons[key] = r
	}
	return false
}

// SortedPakPaths returns the recorded pak paths in stable order — handy for
// stable logging / progress output across runs.
func (idx *RibbonIndex) SortedPakPaths() []string {
	out := make([]string, 0, len(idx.Paks))
	for p := range idx.Paks {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
