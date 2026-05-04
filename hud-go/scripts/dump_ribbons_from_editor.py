# Editor-side dump: produces the ribbons CSV that ribbons-to-geojson.exe reads.
#
# Usage:
#   1. Open the route in the TSW PC Editor
#   2. Window > Developer Tools > Output Log; switch the bottom dropdown to
#      Python
#   3. Edit the `ROUTE_PATH` and `out_path` constants below for the loaded
#      route, then paste-and-run the rest in the Python console
#
# The script:
#   - Force-loads every TT_*.umap streaming sublevel so its sub-objects
#     (NetworkRibbon, NetworkNode, NetworkTurnoutJunction) become accessible
#   - Walks every TrackNetworkActor.Ribbons array
#   - For each NetworkRibbon, brute-force pulls every UPROPERTY we need
#     (Python's dir() doesn't enumerate them, but get_editor_property("name")
#     works as long as the C++ field is BlueprintReadable)
#   - Emits one CSV row per ribbon: the actor path, ribbon name, GUIDs,
#     curve class, the curve's StartPosition2D / StartTangent2D / Length /
#     Radius, and the ribbon's WorldLocation (the per-ribbon offset that
#     turns local coords into world coords)
#
# The "WorldLocation + StartPosition2D = world cm" recipe is the engine's
# authoritative answer — no per-tile heuristics needed downstream. See
# the rail-pipeline memory notes for the full backstory.

import unreal

# ===== EDIT FOR EACH ROUTE =====
# Path under which this route's TT_*.umap tiles live. Found by browsing the
# Content Browser; for TC it's "/TrainingCentre/Map/Tiles", for HSC it's
# "/HorseShoeCurve/Map/Tiles", for WCML it's "/EustonMiltonKeynes/Map/Tiles".
ROUTE_PATH = r"/HorseShoeCurve/Map/Tiles"

# CSV destination. Must match what ribbons-to-geojson.exe is pointed at.
out_path = r"C:\Users\hcfai\Desktop\HSC_ribbons_canonical.csv"
# ================================


def gx(g):
    """Format an FGuid as 32-char lowercase hex. The Python wrapper exposes
    .a/.b/.c/.d as int32 components; we mask to uint32 before formatting so
    negative ints don't become 0xFFFFFFFF... overflows."""
    def u(x):
        return x & 0xFFFFFFFF
    return (
        f"{u(g.get_editor_property('a')):08x}"
        f"{u(g.get_editor_property('b')):08x}"
        f"{u(g.get_editor_property('c')):08x}"
        f"{u(g.get_editor_property('d')):08x}"
    )


# Force-load TT tile sublevels so their TrackNetworkActor sub-objects (and
# the Ribbons array on each) actually surface in get_all_level_actors().
ar = unreal.AssetRegistryHelpers.get_asset_registry()
ed_world = unreal.EditorLevelLibrary.get_editor_world()
n_made_visible = 0
for ad in ar.get_assets_by_path(ROUTE_PATH, recursive=False):
    name = str(ad.asset_name)
    if not name.startswith("TT_"):
        continue
    sl = unreal.GameplayStatics.get_streaming_level(ed_world, name)
    if sl is None:
        continue
    if not sl.get_editor_property("should_be_visible"):
        sl.set_editor_property("should_be_visible", True)
        n_made_visible += 1
print(f"TT tiles made visible: {n_made_visible}")


# Dump every NetworkRibbon to CSV.
n_act = n_rib = 0
with open(out_path, "w") as f:
    f.write(
        "actor_path,ribbon_name,ribbon_guid,start_node_guid,end_node_guid,"
        "curve_class,sx_cm,sy_cm,tx,ty,length_cm,radius_cm,"
        "world_loc_x,world_loc_y\n"
    )
    for a in unreal.EditorLevelLibrary.get_all_level_actors():
        if a.get_class().get_name() != "TrackNetworkActor":
            continue
        n_act += 1
        for rib in (a.get_editor_property("Ribbons") or []):
            n_rib += 1
            rg = gx(rib.get_editor_property("RibbonGuid"))
            sn = gx(rib.get_editor_property("StartNodeGuid"))
            en = gx(rib.get_editor_property("EndNodeGuid"))
            wl = rib.get_editor_property("WorldLocation")
            wlx, wly = (wl.x, wl.y) if wl else (0, 0)
            curve = rib.get_editor_property("Curve")
            if not curve:
                f.write(
                    f"{a.get_path_name()},{rib.get_name()},{rg},{sn},{en},"
                    f"NoCurve,,,,,,,{wlx},{wly}\n"
                )
                continue
            cls = curve.get_class().get_name()
            sp = curve.get_editor_property("StartPosition2D")
            st = curve.get_editor_property("StartTangent2D")
            length = curve.get_editor_property("Length")
            rad = ""
            try:
                rv = curve.get_editor_property("Radius")
                if rv is not None:
                    rad = f"{rv:.3f}"
            except Exception:
                pass
            f.write(
                f"{a.get_path_name()},{rib.get_name()},{rg},{sn},{en},{cls},"
                f"{sp.x:.3f},{sp.y:.3f},{st.x:.6f},{st.y:.6f},"
                f"{length:.3f},{rad},{wlx},{wly}\n"
            )
print(f"actors: {n_act}, ribbons: {n_rib}, file: {out_path}")
