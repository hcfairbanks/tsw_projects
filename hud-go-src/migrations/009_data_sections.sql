-- Migration 009: Data for sections
-- Rows 1 to 14 of 14

PRAGMA foreign_keys = OFF;

BEGIN TRANSACTION;

INSERT INTO [sections] (id, route_id, name) VALUES (1, 85, 'Boston - Providence HSP-46 Timetable');
INSERT INTO [sections] (id, route_id, name) VALUES (2, 85, 'Boston - Providence Timetable');
INSERT INTO [sections] (id, route_id, name) VALUES (3, 86, 'Bakerloo Line 2021');
INSERT INTO [sections] (id, route_id, name) VALUES (4, 86, 'London Underground Bakerloo Line');
INSERT INTO [sections] (id, route_id, name) VALUES (5, 89, 'Great Western Express 2016');
INSERT INTO [sections] (id, route_id, name) VALUES (6, 89, 'Great Western Express 2017');
INSERT INTO [sections] (id, route_id, name) VALUES (7, 97, 'West Somerset Railway Diesel Gala 2017');
INSERT INTO [sections] (id, route_id, name) VALUES (8, 97, 'West Somerset Railway Steam Gala 2023');
INSERT INTO [sections] (id, route_id, name) VALUES (9, 83, 'ACS-64 V AMTK50');
INSERT INTO [sections] (id, route_id, name) VALUES (10, 83, 'Acela Express Amtrak');
INSERT INTO [sections] (id, route_id, name) VALUES (11, 83, 'CTC-3 Cab Car');
INSERT INTO [sections] (id, route_id, name) VALUES (12, 83, 'F40PH-3C MBTA');
INSERT INTO [sections] (id, route_id, name) VALUES (13, 83, 'Rotem CTC-5 MBTA');
INSERT INTO [sections] (id, route_id, name) VALUES (14, 94, 'GP38-2 CN');

COMMIT;

PRAGMA foreign_keys = ON;
