# tsw6-timetable

Extract service-mode timetable data from **Train Sim World 6** game files and export it as JSON or CSV.

The tool unpacks the game's `.pak` archives, converts the relevant `.uasset` files to JSON via UAssetGUI, then parses the resulting binary property tree into a structured timetable (services, schedules, stop locations, times, and ribbon/platform references).

---

## Prerequisites

| Tool | Purpose | Where to get it |
|---|---|---|
| **TSW6** | Installed via Steam | Steam |
| **repak.exe** *(preferred)* | Unpacks Oodle-compressed `.pak` files | https://github.com/trumank/repak/releases |
| **UAssetGUI.exe** | Converts `.uasset` → JSON | https://github.com/atenfyr/UAssetGUI/releases |
| **UnrealPak.exe** *(fallback)* | Legacy pak extractor (no Oodle support) | Ships with the TSW Editor or any UE4.27 install |
| **.NET 8 Desktop Runtime** | Required by UAssetGUI | https://dotnet.microsoft.com/download |

Place `repak.exe` and `UAssetGUI.exe` next to `tsw6-timetable.exe` and they will be picked up automatically. Otherwise pass them with `--repak` / `--uassetgui`.

---

## Quick start

List available routes in your TSW6 install:

```
tsw6-timetable.exe list --tsw "C:\Program Files (x86)\Steam\steamapps\common\Train Sim World 6"
```

Extract every timetable to a JSON file:

```
tsw6-timetable.exe extract ^
    --tsw "C:\Program Files (x86)\Steam\steamapps\common\Train Sim World 6" ^
    --out timetable.json
```

Extract a single route to CSV:

```
tsw6-timetable.exe extract ^
    --tsw "C:\Program Files (x86)\Steam\steamapps\common\Train Sim World 6" ^
    --route IsleOfWight ^
    --format csv ^
    --out iow.csv
```

---

## Output location

- With `--out PATH` → writes to `PATH` (absolute, or relative to your current shell directory).
- Without `--out` → writes to **stdout**. Redirect to a file with `> timetable.json` if needed.
- **There is no default "write next to the .exe" behaviour** — you must specify a path or redirect.

---

## Commands

### `list`
Enumerate routes that `tsw6-timetable` can see in the TSW6 install.

| Flag | Required | Description |
|---|---|---|
| `--tsw` | yes | Path to the TSW6 Steam install directory |

### `extract`
Run the full `pak → uasset → JSON → struct` pipeline.

| Flag | Required | Default | Description |
|---|---|---|---|
| `--tsw` | yes | — | Path to the TSW6 Steam install directory |
| `--out` | no | stdout | Output file path |
| `--format` | no | `json` | `json` or `csv` |
| `--route` | no | — | Substring filter (case-insensitive) on route pak name |
| `--repak` | no | auto | Path to `repak.exe` (preferred) |
| `--uassetgui` | no | auto | Path to `UAssetGUI.exe` |
| `--unrealpak` | no | auto | Path to `UnrealPak.exe` (fallback if `repak` is unavailable) |
| `--aeskey` | no | — | Hex AES-256 key for encrypted paks (no `0x` prefix) |
| `--verbose` | no | false | Print progress to stderr |
| `--debug` | no | false | Keep the temporary work dir and print its path |

---

## Output schema (JSON)

Top-level: an array of `Timetable` objects, one per route.

```jsonc
[
  {
    "route": "IsleOfWight",
    "asset_path": "...\\IOW_Timetable.uasset",
    "services": [
      {
        "name": "2R06",
        "headcode": "2R06",
        "friendly_name": "Ryde Pier Head to Shanklin",
        "service_number": "2R06",
        "service_operator": "South Western Railway",
        "formation": "Class 484",
        "is_player_drivable": true,
        "is_hidden": false,
        "start_time": "08:15:00",
        "schedule": [
          {
            "action": "STOP AT LOCATION",
            "location": "Ryde Pier Head Platform 1",
            "time1": "08:15:00",
            "time2": "08:17:00",
            "sort_order": 0,
            "ribbon_guid": "01AF82CC-49A3D11C-8DD81F89-E108CF0E",
            "ribbon_location": 0.6124
          }
        ]
      }
    ]
  }
]
```

### ScheduleItem fields

| Field | Type | Meaning |
|---|---|---|
| `action` | string | e.g. `STOP AT LOCATION`, `GO VIA LOCATION`, `CHANGE FORMATION` |
| `details` | string | Free-form detail from the instruction |
| `location` | string | Human-readable stop name (e.g. `Ryde Pier Head Platform 1`) |
| `time1` | `HH:MM:SS` | Scheduled arrival (stops) or pass (vias) |
| `time2` | `HH:MM:SS` | Scheduled departure (stops only) |
| `sort_order` | int | Order of the item within the service |
| `structure` / `structure_number` | string | Route-structure metadata, when present |
| `ribbon_guid` | string | UE4 `NetworkRibbon` GUID that the stop refers to — **unique per track/platform** |
| `ribbon_location` | float | Normalised position along the ribbon (`0.0`–`1.0`) = the programmed stop point |

> Two services stopping at the same platform share the same `ribbon_guid` and `ribbon_location`. Different platforms at the same station have different `ribbon_guid`s.

---

## How it works

1. **Discover routes** — scan `TSWPath\IsleOfWight\TS2Prototype\Plugins\DLC\*\Content\Paks\*.pak`.
2. **Unpack each pak** — `repak unpack` (preferred) or `UnrealPak -Extract`. Oodle-compressed paks *require* `repak`.
3. **Locate timetable assets** — walk the extracted tree for `.uasset` files whose path contains `Timetable`, `ServiceMode`, or `service_mode`.
4. **Convert to JSON** — invoke `UAssetGUI.exe tojson <uasset> <json> VER_UE4_27`.
5. **Parse the binary property stream** — the per-export `Data` field in the JSON is a base64-encoded UE4 tagged property blob; the parser walks it directly.
6. **Emit JSON or CSV** — via `encoding/json` or the built-in CSV writer.

Temp files live under `%TEMP%\tsw6-timetable-*\` and are removed unless `--debug` is set.

---

## Troubleshooting

| Symptom | Likely cause / fix |
|---|---|
| `UAssetGUI not found` | Put `UAssetGUI.exe` next to `tsw6-timetable.exe`, or pass `--uassetgui PATH` |
| `no pak extractor found` | Put `repak.exe` next to `tsw6-timetable.exe`, or pass `--repak PATH` |
| `UnrealPak failed` during unpack | The pak is Oodle-compressed — switch to `repak.exe` (UnrealPak cannot read Oodle) |
| Empty output / `No timetable data found.` | Your `--tsw` path is wrong, the route DLC isn't installed, or the pak is encrypted (pass `--aeskey`) |
| UAssetGUI errors re: .NET | Install the **.NET 8 Desktop Runtime** |

Run with `--verbose --debug` to print progress and leave the temp dir on disk for inspection.

---

## Building from source

```
go build -o tsw6-timetable.exe .
```

Go 1.22+ is required. The project has one direct external dep (`github.com/spf13/cobra`).
