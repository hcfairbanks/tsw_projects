-- Migration 006: Data for weather_presets
-- Rows 1 to 3 of 3

PRAGMA foreign_keys = OFF;

BEGIN TRANSACTION;

INSERT INTO [weather_presets] (id, name, temperature, cloudiness, precipitation, wetness, ground_snow, piled_snow, fog_density, created_at) VALUES (1, 'Sunny Day', 25.0, 0.1, 0.0, 0.0, 0.0, 0.0, 0.0, '2026-01-28 13:40:54');
INSERT INTO [weather_presets] (id, name, temperature, cloudiness, precipitation, wetness, ground_snow, piled_snow, fog_density, created_at) VALUES (2, 'Snowy Day', -5.0, 0.8, 0.7, 0.3, 0.8, 0.6, 0.2, '2026-01-28 13:40:54');
INSERT INTO [weather_presets] (id, name, temperature, cloudiness, precipitation, wetness, ground_snow, piled_snow, fog_density, created_at) VALUES (3, 'Rainy Day', 12.0, 0.9, 0.8, 0.7, 0.0, 0.0, 0.3, '2026-01-28 13:40:54');

COMMIT;

PRAGMA foreign_keys = ON;
