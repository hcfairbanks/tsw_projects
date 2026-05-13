-- Migration 003: Data for train_classes
-- Rows 1 to 22 of 22

PRAGMA foreign_keys = OFF;

BEGIN TRANSACTION;

INSERT INTO [train_classes] (id, name, in_game_name) VALUES (4, '1972 Mark 2 Stock', NULL);
INSERT INTO [train_classes] (id, name, in_game_name) VALUES (5, 'Class 377/2 SN', 'Class 377/2 SN');
INSERT INTO [train_classes] (id, name, in_game_name) VALUES (38, 'ACS-64 V AMTK50', 'ACS-64 V AMTK50');
INSERT INTO [train_classes] (id, name, in_game_name) VALUES (39, 'Acela Express Amtrak', 'Acela Express Amtrak');
INSERT INTO [train_classes] (id, name, in_game_name) VALUES (40, 'CTC-3 MBTA', 'CTC-3 MBTA');
INSERT INTO [train_classes] (id, name, in_game_name) VALUES (41, 'F40PH-3C MBTA', 'F40PH-3C MBTA');
INSERT INTO [train_classes] (id, name, in_game_name) VALUES (62, 'LMS Jubilee', NULL);
INSERT INTO [train_classes] (id, name, in_game_name) VALUES (75, 'TGV Duplex', NULL);
INSERT INTO [train_classes] (id, name, in_game_name) VALUES (81, '1972 MkII Tube Stock LU', '1972 MkII Tube Stock LU');
INSERT INTO [train_classes] (id, name, in_game_name) VALUES (91, 'GP38-2 CN', NULL);
INSERT INTO [train_classes] (id, name, in_game_name) VALUES (92, 'GP9RM CN', NULL);
INSERT INTO [train_classes] (id, name, in_game_name) VALUES (113, 'LMS Stanier 8F', NULL);
INSERT INTO [train_classes] (id, name, in_game_name) VALUES (123, 'ABe 8/12', NULL);
INSERT INTO [train_classes] (id, name, in_game_name) VALUES (124, 'Class 66 DB', 'Class 66 DB');
INSERT INTO [train_classes] (id, name, in_game_name) VALUES (131, 'EMD F125 Metrolink', 'EMD F125 Metrolink');
INSERT INTO [train_classes] (id, name, in_game_name) VALUES (132, 'Rotem Bi-Level Cab Car Metrolink', 'Rotem Bi-Level Cab Car Metrolink');
INSERT INTO [train_classes] (id, name, in_game_name) VALUES (146, 'Class 350/1', 'Class 350/1');
INSERT INTO [train_classes] (id, name, in_game_name) VALUES (150, 'Class 150/2 GWR', NULL);
INSERT INTO [train_classes] (id, name, in_game_name) VALUES (157, 'Class 802 GWR', NULL);
INSERT INTO [train_classes] (id, name, in_game_name) VALUES (162, 'Class 390 AWC', 'Class 390');
INSERT INTO [train_classes] (id, name, in_game_name) VALUES (163, 'HSP46 MBTA', 'HSP46 MBTA');
INSERT INTO [train_classes] (id, name, in_game_name) VALUES (164, 'Rotem CTC-5 MBTA', 'Rotem CTC-5 MBTA');

COMMIT;

PRAGMA foreign_keys = ON;
