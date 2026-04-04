-- Migration 015: Data for station_name_mappings
-- Rows 1 to 4 of 4

PRAGMA foreign_keys = OFF;

BEGIN TRANSACTION;

INSERT INTO [station_name_mappings] (id, route_id, display_name, api_name, created_at) VALUES (1, 67, 'London Euston', 'Euston', '2026-01-28 13:40:54');
INSERT INTO [station_name_mappings] (id, route_id, display_name, api_name, created_at) VALUES (2, 67, 'Bletchley Platform', 'Bletchley', '2026-01-28 13:40:54');
INSERT INTO [station_name_mappings] (id, route_id, display_name, api_name, created_at) VALUES (3, 67, 'Watford Junction', 'Watford Jn', '2026-01-28 13:40:54');
INSERT INTO [station_name_mappings] (id, route_id, display_name, api_name, created_at) VALUES (4, 67, 'Milton Keynes', 'MiltonKyns', '2026-01-28 13:40:54');

COMMIT;

PRAGMA foreign_keys = ON;
