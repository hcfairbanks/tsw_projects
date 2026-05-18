package catalog

import "sort"

// TreeNode is one row in the /extractor list tree. Top-level rows are
// parent routes (HasRouteDef=true); their `Children` slice holds the
// addon paks (cargo / wagon / train DLCs) that reference this parent
// in any of their timetables / scenarios.
//
// An addon that references multiple installed parents appears under
// each of them. Addons whose only references are to unowned parents
// are dropped — see BuildTree's filter.
type TreeNode struct {
	PakPath     string `json:"pak_path"`
	Codename    string `json:"name"` // historic field name the UI binds to
	DisplayName string `json:"display_name"`
	Country     string `json:"country,omitempty"`
	Kind        string `json:"kind"` // "route" or "addon"

	// Zip-presence + completed flag — meaningful only on parent (route)
	// nodes since the per-parent extraction model bundles all children
	// into the parent's zip. Both default-zero on addon children and
	// elide via omitempty.
	ZipExists bool  `json:"zip_exists,omitempty"`
	ZipBytes  int64 `json:"zip_bytes,omitempty"`
	ZipMTime  int64 `json:"zip_mtime,omitempty"`
	Completed bool  `json:"completed,omitempty"`

	// Children are addon paks attached under this parent. Always nil
	// for addon nodes themselves (no nesting beyond one level).
	Children []*TreeNode `json:"children,omitempty"`
}

// BuildTree turns a flat catalog slice into the /extractor list tree.
// Two passes:
//
//  1. Walk all paks, partition into parents (HasRouteDef=true) and
//     addons. Build a parent-by-cross_pak_reference_name index.
//  2. Walk addons; for each, attach a copy of itself under every
//     installed parent its CrossPakRefs point at. Addons that don't
//     match any installed parent are silently dropped — they reference
//     only unowned routes, so we don't surface them.
//
// Each parent is sorted by DisplayName; children under each parent are
// also DisplayName-sorted. Stable so the UI order doesn't jitter
// across requests.
//
// Use BuildTreeWithOrphans if the caller also needs the standalone
// Train DLCs (HasRouteDef=false paks whose refs don't match any
// installed parent) — those are otherwise dropped here.
func BuildTree(paks []*Pak) []*TreeNode {
	routes, _ := BuildTreeWithOrphans(paks)
	return routes
}

// BuildTreeWithOrphans is BuildTree plus a second slice of orphan
// addons — paks with HasRouteDef=false whose CrossPakRefs don't match
// any installed parent. These are standalone Train DLCs: rolling stock
// the user has installed without owning the route they were marketed
// for. The /extractor UI renders them as their own section so the user
// can extract their train data without an associated route.
//
// `routes` is the same tree BuildTree returns (parent routes with
// nested addon children).
// `orphans` is the flat list of leftover paks, deduped + DisplayName
// sorted. Each orphan's Kind is "train_dlc" so the UI can style/route
// it separately from regular addons.
func BuildTreeWithOrphans(paks []*Pak) (routes, orphans []*TreeNode) {
	parentByRef := make(map[string]*TreeNode, len(paks))
	var top []*TreeNode

	for _, p := range paks {
		if !p.HasRouteDef {
			continue
		}
		node := &TreeNode{
			PakPath:     p.PakPath,
			Codename:    p.Codename,
			DisplayName: displayLabel(p),
			Country:     p.CountryCode,
			Kind:        "route",
		}
		top = append(top, node)
		if p.CrossPakReferenceName != "" {
			parentByRef[p.CrossPakReferenceName] = node
		}
	}

	// Track which addon paks got attached to at least one parent.
	// Anything left over after this loop is an orphan train DLC.
	attached := make(map[string]struct{}, len(paks))
	for _, p := range paks {
		if p.HasRouteDef {
			continue
		}
		for _, ref := range p.CrossPakRefs {
			parent, ok := parentByRef[ref]
			if !ok {
				continue
			}
			child := &TreeNode{
				PakPath:     p.PakPath,
				Codename:    p.Codename,
				DisplayName: displayLabel(p),
				Country:     p.CountryCode,
				Kind:        "addon",
			}
			parent.Children = append(parent.Children, child)
			attached[p.PakPath] = struct{}{}
		}
	}

	// Pick up the orphans (addons that didn't end up under any parent).
	for _, p := range paks {
		if p.HasRouteDef {
			continue
		}
		if _, ok := attached[p.PakPath]; ok {
			continue
		}
		orphans = append(orphans, &TreeNode{
			PakPath:     p.PakPath,
			Codename:    p.Codename,
			DisplayName: displayLabel(p),
			Country:     p.CountryCode,
			Kind:        "train_dlc",
		})
	}

	sort.SliceStable(top, func(i, j int) bool {
		return top[i].DisplayName < top[j].DisplayName
	})
	for _, parent := range top {
		seen := make(map[string]struct{}, len(parent.Children))
		uniq := parent.Children[:0]
		for _, c := range parent.Children {
			if _, ok := seen[c.PakPath]; ok {
				continue
			}
			seen[c.PakPath] = struct{}{}
			uniq = append(uniq, c)
		}
		parent.Children = uniq
		sort.SliceStable(parent.Children, func(i, j int) bool {
			return parent.Children[i].DisplayName < parent.Children[j].DisplayName
		})
	}
	sort.SliceStable(orphans, func(i, j int) bool {
		return orphans[i].DisplayName < orphans[j].DisplayName
	})
	return top, orphans
}

// displayLabel falls back to the codename when no canonical name is
// available — keeps the row visible (and clickable) instead of blank.
func displayLabel(p *Pak) string {
	if p.DisplayName != "" {
		return p.DisplayName
	}
	return p.Codename
}
