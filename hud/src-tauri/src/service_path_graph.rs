//! Ribbon-graph Dijkstra fallback for services with no DataTrack. Used
//! when the DataTrack-driven [`crate::service_path::build_service_path`]
//! has nothing to slice against — happens on a small minority of TSW2
//! ports and a few one-off services. Mirrors hud-go's
//! `output/service_path.go::BuildServicePath` collapsed to the
//! rails-builder-vertex slice path; the analytical fallback hud-go
//! keeps for legacy callers isn't reproduced here because every hud
//! caller already has the per-ribbon vertex map from
//! [`crate::cookedmap::RouteFeatures::per_ribbon`].
//!
//! Inputs:
//!   * Schedule items with ribbon GUID + fraction (parsed by
//!     `uasset_timetable::parse_cooked_timetable`).
//!   * The route's `ribbon_meta` (length + node GUIDs) and `switches`
//!     records, plus the rails-builder vertex map — all from
//!     [`crate::cookedmap::build`].
//!
//! Output: `Vec<ServiceCoord>` in the same shape
//! `crate::service_path::build_service_path` returns, so the pipeline
//! can route either source into the same encoder + DB write.

use std::cmp::Ordering;
use std::collections::{BinaryHeap, HashMap, HashSet};

use crate::cookedmap::RibbonMeta;
use crate::service_path::ServiceCoord;
use crate::uasset::normalize_guid;
use crate::uasset_features_cooked::Switch;

/// One schedule waypoint reduced to what the path-builder needs.
#[derive(Debug, Clone)]
pub struct Waypoint {
    pub ribbon_guid_norm: String,
    pub fraction:         f32,
}

/// Build the per-service polyline by Dijkstra-routing between schedule
/// waypoints. Returns an empty vec when the schedule has no waypoints
/// with usable ribbon refs (e.g. cargo-yard services with no
/// LinkedPlatform anchors).
pub fn build_service_path(
    schedule:   &[Waypoint],
    ribbons:    &[RibbonMeta],
    switches:   &[Switch],
    per_ribbon: &HashMap<String, Vec<[f64; 2]>>,
) -> Vec<ServiceCoord> {
    // Filter to waypoints whose ribbon is in the rails map. A waypoint
    // whose ribbon was filtered out (length==0 etc.) can't seed a
    // segment; we just skip it.
    let wps: Vec<&Waypoint> = schedule.iter()
        .filter(|w| !w.ribbon_guid_norm.is_empty() && per_ribbon.contains_key(&w.ribbon_guid_norm))
        .collect();
    if wps.is_empty() { return Vec::new(); }

    let ribbon_by_guid: HashMap<&str, &RibbonMeta> = ribbons.iter()
        .map(|r| (r.ribbon_guid.as_str(), r))
        .collect();
    let adj = build_node_adjacency(ribbons, switches, &ribbon_by_guid);

    let mut out: Vec<ServiceCoord> = Vec::new();
    // Seed with the first waypoint's snapped position.
    if let Some(p) = snap_waypoint(wps[0], per_ribbon) {
        push_unique(&mut out, p);
    }

    for i in 1..wps.len() {
        let prev = wps[i - 1];
        let cur  = wps[i];
        let seg = sample_path_between(prev, cur, &ribbon_by_guid, &adj, per_ribbon);
        // Drop the duplicate seed at the join.
        let start_idx = if !seg.is_empty() && !out.is_empty()
            && out.last().unwrap().latitude  == seg[0].latitude
            && out.last().unwrap().longitude == seg[0].longitude
        { 1 } else { 0 };
        for c in &seg[start_idx..] { push_unique(&mut out, c.clone()); }
    }
    out
}

#[inline]
fn push_unique(out: &mut Vec<ServiceCoord>, v: ServiceCoord) {
    if let Some(last) = out.last() {
        if last.latitude == v.latitude && last.longitude == v.longitude { return; }
    }
    out.push(v);
}

/// Slice or route between two waypoints. Same-ribbon → direct slice;
/// cross-ribbon → Dijkstra over the node adjacency, then concatenate.
fn sample_path_between(
    prev: &Waypoint, cur: &Waypoint,
    ribbon_by_guid: &HashMap<&str, &RibbonMeta>,
    adj: &HashMap<String, Vec<EdgeRole>>,
    per_ribbon: &HashMap<String, Vec<[f64; 2]>>,
) -> Vec<ServiceCoord> {
    if prev.ribbon_guid_norm == cur.ribbon_guid_norm {
        return slice_ribbon(&cur.ribbon_guid_norm, prev.fraction, cur.fraction, per_ribbon);
    }
    let Some(start_rib) = ribbon_by_guid.get(prev.ribbon_guid_norm.as_str()) else { return Vec::new() };
    let Some(end_rib)   = ribbon_by_guid.get(cur.ribbon_guid_norm.as_str())  else { return Vec::new() };
    let Some(chain) = dijkstra_ribbon_path(adj, ribbon_by_guid, start_rib, end_rib) else {
        // No rail-graph route — emit prev tail + cur head so the caller
        // sees two segments stitched (a visible gap on the renderer).
        let mut out: Vec<ServiceCoord> = Vec::new();
        if let Some(p) = snap_waypoint(prev, per_ribbon) { out.push(p); }
        if let Some(p) = snap_waypoint(cur,  per_ribbon) { out.push(p); }
        return out;
    };

    let mut out: Vec<ServiceCoord> = Vec::new();
    let chain_len = chain.len();
    for (i, e) in chain.iter().enumerate() {
        let (from, to) = if i == 0 {
            // First ribbon: from prev.fraction → whichever end joins next.
            let end_frac = if e.role == "end" { 0.0 } else { 1.0 };
            (prev.fraction as f64, end_frac)
        } else if i == chain_len - 1 {
            // Last ribbon: from the entry end → cur.fraction.
            let start_frac = if e.role == "end" { 1.0 } else { 0.0 };
            (start_frac, cur.fraction as f64)
        } else {
            // Intermediate ribbon: traverse fully.
            if e.role == "end" { (1.0, 0.0) } else { (0.0, 1.0) }
        };
        let seg = slice_ribbon(&e.ribbon_guid_norm, from as f32, to as f32, per_ribbon);
        let start_idx = if !seg.is_empty() && !out.is_empty()
            && out.last().unwrap().latitude  == seg[0].latitude
            && out.last().unwrap().longitude == seg[0].longitude
        { 1 } else { 0 };
        for c in &seg[start_idx..] { push_unique(&mut out, c.clone()); }
    }
    out
}

fn snap_waypoint(w: &Waypoint, per_ribbon: &HashMap<String, Vec<[f64; 2]>>) -> Option<ServiceCoord> {
    let pts = per_ribbon.get(&w.ribbon_guid_norm)?;
    if pts.is_empty() { return None; }
    let i = nearest_vertex_index(pts.len(), w.fraction);
    Some(ServiceCoord { longitude: pts[i][0], latitude: pts[i][1] })
}

fn slice_ribbon(
    ribbon_guid_norm: &str,
    from: f32, to: f32,
    per_ribbon: &HashMap<String, Vec<[f64; 2]>>,
) -> Vec<ServiceCoord> {
    let Some(pts) = per_ribbon.get(ribbon_guid_norm) else { return Vec::new() };
    if pts.len() < 2 { return Vec::new(); }
    let i_from = nearest_vertex_index(pts.len(), from);
    let i_to   = nearest_vertex_index(pts.len(), to);
    let mut out: Vec<ServiceCoord> = Vec::new();
    if i_from == i_to {
        out.push(ServiceCoord { longitude: pts[i_to][0], latitude: pts[i_to][1] });
        return out;
    }
    if i_from < i_to {
        for i in i_from..=i_to {
            out.push(ServiceCoord { longitude: pts[i][0], latitude: pts[i][1] });
        }
    } else {
        let mut i = i_from;
        loop {
            out.push(ServiceCoord { longitude: pts[i][0], latitude: pts[i][1] });
            if i == i_to { break; }
            i -= 1;
        }
    }
    out
}

fn nearest_vertex_index(len: usize, frac: f32) -> usize {
    if len == 0 { return 0; }
    let n = (len - 1) as f32;
    let f = frac.clamp(0.0, 1.0);
    let i = (f * n).round() as i64;
    i.clamp(0, n as i64) as usize
}

// =============================================================== adjacency

#[derive(Debug, Clone)]
struct EdgeRole {
    ribbon_guid_norm: String,
    role:             &'static str, // "start" or "end"
}

fn build_node_adjacency(
    ribbons: &[RibbonMeta],
    switches: &[Switch],
    ribbon_by_guid: &HashMap<&str, &RibbonMeta>,
) -> HashMap<String, Vec<EdgeRole>> {
    let mut adj: HashMap<String, Vec<EdgeRole>> = HashMap::new();
    let mut add = |node: &str, ribbon_guid_norm: &str, role: &'static str, adj: &mut HashMap<String, Vec<EdgeRole>>| {
        if node.is_empty() || ribbon_guid_norm.is_empty() { return; }
        let bucket = adj.entry(node.to_string()).or_default();
        for e in bucket.iter() {
            if e.ribbon_guid_norm == ribbon_guid_norm && e.role == role { return; }
        }
        bucket.push(EdgeRole { ribbon_guid_norm: ribbon_guid_norm.to_string(), role });
    };
    for r in ribbons {
        if r.length_cm <= 0.0 { continue; }
        add(&r.start_node_guid, &r.ribbon_guid, "start", &mut adj);
        add(&r.end_node_guid,   &r.ribbon_guid, "end",   &mut adj);
    }
    for s in switches {
        let node = if !s.node_guid.is_empty() { &s.node_guid } else { &s.jct_guid };
        if node.is_empty() { continue; }
        // Ingoing ribbon: train arrives, meets switch at its END node.
        if let Some(rib) = ribbon_by_guid.get(normalize_guid(&s.ingoing_ribbon).as_str()) {
            add(node, &rib.ribbon_guid, "end", &mut adj);
        }
        // Outgoing + turnout: train leaves through them; switch sits at their START.
        if let Some(rib) = ribbon_by_guid.get(normalize_guid(&s.outgoing_ribbon).as_str()) {
            add(node, &rib.ribbon_guid, "start", &mut adj);
        }
        if let Some(rib) = ribbon_by_guid.get(normalize_guid(&s.turnout_ribbon).as_str()) {
            add(node, &rib.ribbon_guid, "start", &mut adj);
        }
    }
    adj
}

// =============================================================== Dijkstra

#[derive(Debug, Clone, Eq, PartialEq, Hash)]
struct StateKey { guid: String, role: &'static str }

#[derive(Debug)]
struct HeapItem { key: StateKey, dist: f64 }

impl Eq for HeapItem {}
impl PartialEq for HeapItem { fn eq(&self, other: &Self) -> bool { self.dist == other.dist } }
impl Ord for HeapItem {
    fn cmp(&self, other: &Self) -> Ordering {
        // BinaryHeap is max-heap; invert so smallest dist pops first.
        other.dist.partial_cmp(&self.dist).unwrap_or(Ordering::Equal)
    }
}
impl PartialOrd for HeapItem { fn partial_cmp(&self, other: &Self) -> Option<Ordering> { Some(self.cmp(other)) } }

#[derive(Debug, Clone)]
struct ChainEdge {
    ribbon_guid_norm: String,
    role: &'static str,
}

fn dijkstra_ribbon_path(
    adj:           &HashMap<String, Vec<EdgeRole>>,
    ribbon_by_guid: &HashMap<&str, &RibbonMeta>,
    start: &RibbonMeta, end: &RibbonMeta,
) -> Option<Vec<ChainEdge>> {
    if start.ribbon_guid == end.ribbon_guid {
        return Some(vec![ChainEdge {
            ribbon_guid_norm: start.ribbon_guid.clone(),
            role: "start",
        }]);
    }
    let mut pq: BinaryHeap<HeapItem> = BinaryHeap::new();
    let mut dist:   HashMap<StateKey, f64>      = HashMap::new();
    let mut prev:   HashMap<StateKey, StateKey> = HashMap::new();
    let mut visited: HashSet<StateKey>          = HashSet::new();

    for role in ["start", "end"] {
        let k = StateKey { guid: start.ribbon_guid.clone(), role };
        dist.insert(k.clone(), 0.0);
        pq.push(HeapItem { key: k, dist: 0.0 });
    }

    let mut end_key: Option<StateKey> = None;
    while let Some(it) = pq.pop() {
        if visited.contains(&it.key) { continue; }
        visited.insert(it.key.clone());

        if it.key.guid == end.ribbon_guid {
            end_key = Some(it.key.clone());
            break;
        }

        let Some(cur_rib) = ribbon_by_guid.get(it.key.guid.as_str()) else { continue };
        // Exit node = opposite of entry role.
        let exit_node = if it.key.role == "start" { &cur_rib.end_node_guid } else { &cur_rib.start_node_guid };
        if exit_node.is_empty() { continue; }
        let new_dist = it.dist + cur_rib.length_cm / 100.0;
        let Some(edges) = adj.get(exit_node) else { continue };
        for e in edges {
            if e.ribbon_guid_norm == cur_rib.ribbon_guid { continue; }
            let next = StateKey { guid: e.ribbon_guid_norm.clone(), role: e.role };
            if visited.contains(&next) { continue; }
            if let Some(&existing) = dist.get(&next) {
                if new_dist >= existing { continue; }
            }
            dist.insert(next.clone(), new_dist);
            prev.insert(next.clone(), it.key.clone());
            pq.push(HeapItem { key: next, dist: new_dist });
        }
    }
    let end_key = end_key?;
    let mut chain: Vec<ChainEdge> = Vec::new();
    let mut cur = end_key;
    loop {
        chain.insert(0, ChainEdge {
            ribbon_guid_norm: cur.guid.clone(),
            role: cur.role,
        });
        let Some(p) = prev.get(&cur) else { break };
        cur = p.clone();
    }
    Some(chain)
}
