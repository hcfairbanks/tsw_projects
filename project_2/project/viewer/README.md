# tsw6 rails viewer

Single-file HTML viewer for inspecting rails GeoJSON and per-service JSON files
that come out of `tsw6-timetable extract --format package`.

## Usage

Open `index.html` directly in a browser (Chrome/Edge/Firefox). No build, no
server needed — everything runs client-side.

1. Pick a `*_rails.geojson` file (extract one from your `*_timetables.zip`).
   The map zooms to the rail network and renders every ribbon as a polyline.
   Click a polyline to see ribbon GUID, length, tile, and anchor status.
2. Optionally pick one or more per-service JSON files (also from the zip).
   Each service is drawn as a blue path connecting its scheduled stops, with a
   red dot at every stop. Click a dot to see arrival/departure times.

The "Clear services" button removes the loaded services without touching the
rails layer.

## Notes

- Ribbon polylines use a 10 m sample step along each ribbon's arc geometry.
  Adjust via `RailsGeoJSONOptions` in `internal/output/rails_geojson.go` if you
  need finer or coarser sampling.
- Anchored ribbons render dark grey, unanchored ones lighter grey. Anchored
  vs. unanchored describes whether the ribbon record carried a WorldLocation;
  both are placed in real-world coordinates either way.
- Service polylines use `timetable[]` lat/lng fields. These are stop markers,
  not the actual track-following path — that's a future enhancement (Tier 2/3
  in the rail-path drawing plan).
