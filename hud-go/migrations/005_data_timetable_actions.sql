-- Migration 005: Data for timetable_actions
-- Rows 1 to 8 of 8

PRAGMA foreign_keys = OFF;

BEGIN TRANSACTION;

INSERT INTO [timetable_actions] (id, name) VALUES (1, 'WAIT FOR SERVICE');
INSERT INTO [timetable_actions] (id, name) VALUES (2, 'WAIT');
INSERT INTO [timetable_actions] (id, name) VALUES (3, 'STOP AT LOCATION');
INSERT INTO [timetable_actions] (id, name) VALUES (4, 'LOAD PASSENGERS');
INSERT INTO [timetable_actions] (id, name) VALUES (5, 'UNLOAD PASSENGERS');
INSERT INTO [timetable_actions] (id, name) VALUES (6, 'GO VIA LOCATION');
INSERT INTO [timetable_actions] (id, name) VALUES (7, 'UNCOUPLE VEHICLES');
INSERT INTO [timetable_actions] (id, name) VALUES (8, 'COUPLE TO FORMATION');

COMMIT;

PRAGMA foreign_keys = ON;
