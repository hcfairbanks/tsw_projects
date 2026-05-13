# TSW6 API ↔ Pak Data Integration Spec

**Audience:** another Claude session (or developer) picking this up later. Written 2026-04-23 while driving service 1A90 on the Riviera Line.

**Purpose:** define how live data from the TSW6 local API connects to our pak-extracted service database so that, as soon as a player joins a service, the HUD can auto-populate:

1. **Train class** — the exact stock label the player sees (e.g. "GWR Class 802", "Rotem CTC-5 MBTA")
2. **Service** — the scheduled service record (schedule, stops, start time, duration, conductor_compatible, etc.)
3. **Consist** — the ordered list of vehicles (per-vehicle class, length, position)

Plus a forward-looking section on computing the correct stopping position at each platform from consist length.

---

## 1. Identity keys

There are exactly two join keys between the API and our pak extraction:

| Dimension | API endpoint & field | Our pak field (timetable binary) | Our output JSON field |
|---|---|---|---|
| Service | `GET /get/DriverAid.PlayerInfo` → `Values.currentServiceName` | `Services[].Name` (NameProperty) | `current_service_name` |
| Vehicle | `GET /get/CurrentFormation/{i}.VehicleID` → `Values.VehicleID` | `Formations[].RailVehicleInfo[i].RailVehicleID` (StructProperty, Guid) | stored inside the formation's vehicle list |

Both are string-valued and match byte-for-byte. No normalisation required.

### Service name nuances

- **`currentServiceName`** is the backend ID — on BOW it's short (`"MBTA-508"`), on Boston Providence it's long (`"MBTA Franklin #701 (Inbound)"`), on Riviera it's a TOPS headcode (`"1A90"`).
- **`level_info.service_name`** (captured by the bot, never by the API) is the friendly UI name shown in the service picker. It corresponds to our `Service.FriendlyName` (TextProperty `CultureInvariantString`).
- Our output's `current_service_name` field equals `Service.Name` exactly — do not synthesise a short ID from operator+number (see [bot_service_name_fields memory](../../../AppData/Local/Claude/...)).

### VehicleID GUID format

The API returns a 32-char hex string: `"0C4776504D9DB72131A550859ACC6EFC"`.
Our parser stores it as `"0C477650-4D9DB721-31A55085-9ACC6EFC"` (dashed 4-group form) in CompiledRVMap keys. **Strip the dashes before comparison** (or keep them in both formats — either works, just stay consistent).

---

## 2. Lookup chain

Starting from the live API, one query chain fills all three dimensions:

```
API: currentServiceName = "1A90"
  │
  └─> DB.services.name = "1A90"
        ├─ friendly_name         ← Service.FriendlyName
        ├─ route                 ← route pak name
        ├─ service_type          ← simplified ServiceClass
        ├─ conductor_compatible  ← computed rule (see conductor_compatible_rule memory)
        ├─ start_time, duration
        ├─ train_classes[]       ← derived from consist (see train_class_derivation memory)
        └─ formation_name        ← Service.Formation (FK to formations table)

API: for i in 0..N-1:
  CurrentFormation/{i}.VehicleID = "0C4776504D9DB72131A550859ACC6EFC"
  │
  └─> DB.formation_vehicles WHERE rail_vehicle_id = that GUID AND formation_name = svc.formation_name
        ├─ slot_index (0..N-1)    — matches the API's {i}
        ├─ max_length_m           — from FormationVehicle.MaxLengthM
        ├─ extension_length_m     — from FormationVehicle.ExtensionLengthM
        ├─ flipped                — from FormationVehicle.Flipped
        └─ rvd_asset_path         — FK to rvds table (via CompiledRVMap resolution)

DB.rvds WHERE canonical_path = rvd_asset_path
  ├─ friendly_name          ← RVD.FriendlyName (UI class label, e.g. "Rotem CTC-5 MBTA")
  ├─ livery_id              ← RVD.LiveryID
  ├─ vehicle_category       ← RVD.VehicleCategory
  ├─ service_types          ← RVD.ServiceTypes (bitmask)
  ├─ has_guard_controls     ← RVD.bHasGuardModeControls
  ├─ substitutable_unit     ← RVD.bIsSubstitutableUnit
  └─ available_regions[]    ← RVD.AvailableGeographicRegions
```

Per-vehicle live position is also available:
```
API: CurrentFormation/{i}.LatLon → {Lat, Lon}
```
which returns WGS84 lat/lng directly; no conversion needed for the live side (the pak→lat/lng conversion is only needed for pre-computing schedule stop locations — see §4).

---

## 3. Suggested database schema

Minimal relational schema sufficient for the HUD's needs:

```sql
-- Services scheduled per route, one row per Services[] entry across all timetables
CREATE TABLE services (
  name TEXT PRIMARY KEY,                     -- matches API currentServiceName
  friendly_name TEXT,                        -- UI label
  route TEXT,                                -- route pak name (e.g. "BostonWorcester")
  section_name TEXT,                         -- sub-timetable name
  source TEXT,                               -- "Timetable" | "Scenario" | "Training"
  service_class TEXT,                        -- "Passenger" | "Freight"
  service_type TEXT,                         -- simplified category
  service_operator TEXT,                     -- "MBTA" | "NJT" | "Amtrak" | "None" | ...
  layer_name TEXT,
  formation_name TEXT,                       -- FK to formations.name
  end_of_service_formation TEXT,
  is_player_drivable BOOLEAN,
  is_hidden BOOLEAN,
  player_drivable_side TEXT,                 -- "Front" | "Back"
  description TEXT,
  start_time TEXT,                           -- "HH:MM:SS"
  duration TEXT,                             -- "HH:MM"
  stop_and_load_count INTEGER,
  conductor_compatible BOOLEAN,              -- computed rule
  train_classes_json TEXT                    -- JSON array of FriendlyNames
);

-- Schedule items per service (stops, loading events, reversals, etc.)
CREATE TABLE schedule_items (
  service_name TEXT,
  sort_order INTEGER,
  action TEXT,
  details TEXT,
  location TEXT,
  time1 TEXT, time2 TEXT,
  structure TEXT, structure_number TEXT,
  ribbon_guid TEXT,                          -- binds to network ribbon
  ribbon_location REAL,                      -- position along ribbon (arc length, metres)
  lat REAL, lng REAL,                        -- pre-computed via geo conversion (see world_to_geo_conversion memory)
  PRIMARY KEY (service_name, sort_order),
  FOREIGN KEY (service_name) REFERENCES services(name)
);

-- Formations shared across many services (consist templates)
CREATE TABLE formations (
  name TEXT PRIMARY KEY,
  route TEXT,
  spawn_ribbon_guid TEXT,
  spawn_ribbon_location REAL
);

-- Per-vehicle slots inside a formation (ordered)
CREATE TABLE formation_vehicles (
  formation_name TEXT,
  slot_index INTEGER,                        -- 0..N-1, matches API's CurrentFormation/{i}
  rail_vehicle_id TEXT,                      -- GUID, matches API's VehicleID
  max_length_m REAL,                         -- used for consist-length calcs
  extension_length_m REAL,
  flipped BOOLEAN,
  rvd_path TEXT,                             -- canonical RVD asset path, FK to rvds
  PRIMARY KEY (formation_name, slot_index),
  FOREIGN KEY (formation_name) REFERENCES formations(name),
  FOREIGN KEY (rvd_path) REFERENCES rvds(canonical_path)
);

-- RVD records; global across routes (deduped by canonical path)
CREATE TABLE rvds (
  canonical_path TEXT PRIMARY KEY,           -- e.g. "/BPE_MBTA_SingleDeckCars/Data/.../RVD_BPE_MBTA_CabCar"
  friendly_name TEXT,                        -- "CTC-3 Cab Car" — matches bot folder name
  rail_vehicle_class TEXT,                   -- "CTC-3"
  livery_id TEXT,                            -- "MBTA", "Amtrak", "NJT", ...
  vehicle_category TEXT,                     -- "Locomotive" | "PassengerCabCar" | "PassengerCoach" | ...
  service_types INTEGER,                     -- bitmask: 3=commuter, 5/6=intercity, 1=loco-only
  has_guard_controls BOOLEAN,
  substitutable_unit BOOLEAN,
  available_regions_json TEXT,               -- JSON array
  approximate_length_m REAL,
  drivable BOOLEAN
);
```

**Indexing:** a non-PK index on `formation_vehicles(rail_vehicle_id)` lets you reverse-lookup "which service am I on?" from a single VehicleID, useful if the player joined mid-consist.

**Loading:** dump our Go extractor's package output into these tables. Each per-service JSON file already has the data we need; a small import script maps them row-for-row. If you want to go batch-import via CSV, the existing CSV-format extractor (`--format csv`) gives a columnar view of services.

---

## 4. Auto-connect flow when player joins a service

Trigger: the HUD polls `DriverAid.PlayerInfo` every N seconds (or on a game event). When `currentServiceName` transitions from empty/"None" to a non-empty value:

```
on service_joined(service_name):
  # 1. SERVICE
  svc = db.services[service_name]
  if svc is None:
    # Service not in DB — likely a Gen8 route not yet extracted. Log and fall back.
    hud.show("Service data unavailable for: " + service_name)
    return

  # 2. CONSIST
  formation = db.formations[svc.formation_name]
  slots = db.formation_vehicles WHERE formation_name = svc.formation_name
          ORDER BY slot_index
  consist = []
  for slot in slots:
    # Optional: verify against live API that the vehicle at this index matches
    live_vid = api.get(f"CurrentFormation/{slot.slot_index}.VehicleID")
    if live_vid != slot.rail_vehicle_id:
      # Substitution happened at runtime — resolve the live GUID against rvds
      live_rvd = db.find_rvd_by_guid(live_vid)        # may require a secondary lookup table
      consist.append({slot_index, rvd: live_rvd, flipped: ?, ...})
    else:
      rvd = db.rvds[slot.rvd_path]
      consist.append({slot_index, rvd, flipped: slot.flipped, ...})

  # 3. TRAIN CLASS
  # The "train class" the user cares about is the lead vehicle's FriendlyName —
  # that's what the bot captures and what shows in the stock picker.
  lead_idx = 0 if svc.player_drivable_side == "Front" else len(consist) - 1
  train_class = consist[lead_idx].rvd.friendly_name   # e.g. "GWR Class 802"

  hud.display(
    service = svc,                                    # schedule, stops, start/end, duration
    train_class = train_class,                        # what the user searches by
    consist = consist,                                # full ordered vehicle list for the HUD sidebar
    conductor_compatible = svc.conductor_compatible,
  )
```

**Substitution handling:** the live API is authoritative. If a player loaded the service with a non-default train class (e.g. chose "Rotem CTC-5 MBTA" instead of the scheduled "CTC-3 MBTA"), `CurrentFormation/{i}.VehicleID` reflects the chosen stock. Look up that live GUID in a `rv_id → rvd_path` index built across ALL formations in the DB (not just the scheduled one) to resolve the actual RVD.

---

## 5. Stopping-position inference

**Goal:** tell the driver exactly where to stop so the cab aligns with the correct platform marker. This depends on consist length and the platform's authored stop markers.

### Available data

Per consist vehicle (already in our extraction):
- `max_length_m` + `extension_length_m` — full physical length of the vehicle.

Per schedule `STOP AT LOCATION` item:
- `location` — station name ("Boston South Station")
- `structure` — usually `"Track"` or `"Platform"`
- `structure_number` — track/platform number
- `ribbon_guid` + `ribbon_location` — the exact point on the network where the service is scheduled to come to a standstill.

### Conversion to real-world position

Each schedule item's ribbon position can be converted to lat/lng via the world-to-geo formula (see world_to_geo_conversion memory). That gives the **scheduled stop point** — typically the position of the driver's cab when stopped.

### Using consist length

If the HUD wants to show "stop with cab at X metres into the platform":

```
# Total consist length, in metres
consist_length_m = sum(v.max_length_m + v.extension_length_m for v in consist)

# Scheduled stop point = where the lead cab is expected to be when stopped
stop_point_geo = schedule_item.lat, schedule_item.lng
stop_point_ribbon_location_m = schedule_item.ribbon_location   # arc distance along ribbon

# Rear of the train at stop:
rear_point_ribbon_location_m = stop_point_ribbon_location_m - consist_length_m

# If the rear_point is off the platform, warn the driver or recompute
# with a different alignment (stop further forward).
```

### What's still missing

We don't yet extract **platform extents** (start/end ribbon position per platform). The ribbon segment containing the stop marker is known, but its full ribbon range (platform from arrival end to departure end) is defined elsewhere in the route's level data (usually a `PlatformDefinition` uasset or similar). This is the next piece to parse before stop-position math is fully meaningful.

**Proposed approach** (when ready to tackle):
1. Extend our uasset parser to capture `PlatformDefinition` (or equivalent) records.
2. Add a `platforms` table keyed by station + structure + number, with `ribbon_guid`, `ribbon_start_m`, `ribbon_end_m`, `platform_length_m`.
3. At stop time: if the scheduled cab position + consist length > platform length, the train is too long — advise the driver on the best compromise (typically stop so rear is at the back of the platform, cab is beyond the platform's far end — a "short platform" scenario common in the UK).

Ribbon arc-distance geometry is already being computed for IoW in [tools/compute_positions.py](../tools/compute_positions.py); that tool's approach (NetworkRibbon `Curve.StartPosition2D`, `Radius`, `Length`) generalises to any route.

---

## 6. What works today, by region

| Route format | Services/Formations extracted | API link ready | Blocker |
|---|---|---|---|
| US pre-Gen8 (BOW, BP, Morristown) | Yes | Yes | None |
| UK Gen8 (Riviera, Isle of Wight) | No — only DataTracks extracted | Partial — API works; no DB entries to join against | Gen8 DataTrack parser needed |
| German Gen8 (DresdenLeipzig, DresdenRiesa) | No (also has a legacy mirror we could parse) | Partial | Same as UK Gen8 |

For the Riviera live-test specifically (service 1A90 at the time of writing):
- API works — we captured VehicleIDs and currentServiceName successfully.
- DB-side: the Riviera_Gen8_Timetable_* files are extracted as JSON but our Go parser doesn't understand their `ServiceDataTracks` map structure yet, so `services.name = "1A90"` doesn't exist in the DB. Once Gen8 parsing is added, this all lights up.

---

## 7. Cross-references (other memory entries)

- `conductor_compatible_rule.md` — full derivation of the `conductor_compatible` field
- `train_class_derivation.md` — algorithm for computing `train_classes` from a consist
- `world_to_geo_conversion.md` — tile + within-tile offset → lat/lng (needed to populate `schedule_items.lat/lng`)
- `bot_service_name_fields.md` — `current_service_name` vs `level_info.service_name` distinction

---

## 8. Open questions / next steps (for the future-chat reader)

1. **Database choice** — SQLite is probably enough for a local HUD. If the DB lives on the player's machine, no server needed. Imports trivially from our Go extractor's per-service JSON.
2. **Update cadence** — the extractor output is effectively static per game version. Re-run after a TSW6 update or a DLC install.
3. **Gen8 parsing** — highest-priority extension. Without it, UK/German routes don't appear in the DB and the HUD can't auto-connect for them.
4. **Live substitution index** — when building the RVD table, also build a `vehicle_id → rvd_path` index from every `CompiledRVMap` across every timetable. Needed to resolve a live VehicleID that doesn't match the scheduled formation (player chose a different train class).
5. **Platform extents** — needed before stopping-position inference is useful.
6. **Within-tile offset verification** — `Player.Property.LastPosition` was `{0,0,0}` even while driving 1A90 on Riviera. We don't yet know a reliable API endpoint for the driver's within-tile position. For HUD position display, `CurrentFormation/{i}.LatLon` is sufficient — but for pak → geo conversion of schedule stops, we read `WorldLocation` from the umap files directly, bypassing this entirely.
