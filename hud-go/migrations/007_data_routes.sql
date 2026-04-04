-- Migration 007: Data for routes
-- Rows 1 to 67 of 67

PRAGMA foreign_keys = OFF;

BEGIN TRANSACTION;

INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (25, 'LGV Mediterranee', 4, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (46, 'Spirit of Steam: Liverpool - Crewe', 1, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (55, 'Berninalinie', 6, 4);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (60, 'Antelope Valley Line', 3, 4);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (67, 'WCML South - London Euston to Milton Keynes', 1, 5);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (76, 'Riviera Line', 1, 6);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (81, 'Training Center', 2, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (83, 'Boston Worcester', 3, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (84, 'Morristown Line: New York', 3, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (85, 'Boston Sprinter', 3, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (86, 'London Underground Bakerloo Line', 1, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (88, 'Bahnstrecke Leipzig - Dresden', 2, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (89, 'Great Western Express', 1, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (90, 'Harlem Line', 3, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (91, 'Horseshoe Curve', 3, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (92, 'London Overground: Gospel Oak - Barking', 1, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (93, 'Nahverkehr Dresden', 2, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (94, 'Oakville Subdivision', 9, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (95, 'Sand Patch Grade', 3, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (96, 'Sherman Hill: Cheyenne - Laramie', 3, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (97, 'West Somerset Railway', 1, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (98, 'Arosalinie', 6, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (99, 'Bahnstrecke Bremen - Oldenburg', 2, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (100, 'East Coastway', 1, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (101, 'Cajon Pass', 3, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (102, 'Cardiff City Network', 1, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (103, 'Birmingham Cross-City', 1, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (104, 'Tharandter Rampe: Dresden - Chemnitz', 2, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (105, 'Hauptstrecke Rhein-Ruhr', 2, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (106, 'Edinburgh Glasgow', 1, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (107, 'Clinchfield Railroad: Elkhorn - Dante', 3, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (108, 'S-Bahn Frankfurt', 2, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (109, 'Fife Circle Line', 1, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (110, 'Frankfurt - Fulda: Kinzigtalbahn', 2, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (111, 'Cathcart Circle Line', 1, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (112, 'Hauptstrecke Hamburg - Lübeck', 2, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (113, 'Isle of Wight V2', 1, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (114, 'Isle of Wight', 1, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (115, 'Schnellfahrstrecke Kassel - Würzburg', 2, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (116, 'Schnellfahrstrecke Köln - Aachen', 2, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (117, 'Long Island Rail Road', 3, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (118, 'Linke Rheinstrecke: Mainz - Koblenz', 2, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (119, 'Liberec', 11, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (120, 'Liberec - Stará Paka', 11, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (121, 'London Commuter', 1, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (122, 'Southeastern High Speed', 1, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (123, 'Luzern - Sursee', 6, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (124, 'Hauptstrecke München - Augsburg', 2, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (125, 'Mittenwaldbahn', 2, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (126, 'Maintalbahn: Aschaffenburg - Miltenberg', 2, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (127, 'Glossop Line', 1, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (128, 'Ludwigsbahn: Mannheim - Kaiserslautern', 2, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (129, 'NEC: New York - Trenton', 3, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (130, 'Niddertalbahn', 2, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (131, 'East Coast Main Line: Peterborough - Doncaster', 1, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (132, 'Peak Forest Railway', 1, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (133, 'West Cornwall Local', 1, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (134, 'Blackpool Branches', 1, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (135, 'Bahnstrecke Salzburg - Rosenheim', 2, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (136, 'San Bernardino Line', 3, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (137, 'Semmeringbahn', 5, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (138, 'NEC Metro-North: New York - Stamford', 3, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (139, 'Frankenbahn: Stuttgart - Heilbronn', 2, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (140, 'S-Bahn Vorarlberg', 5, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (141, 'WCML: Preston - Carlisle', 1, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (142, 'Rhein-Ruhr Osten', 2, 3);
INSERT INTO [routes] (id, name, country_id, tsw_version) VALUES (143, 'Spoorlijn Zwolle - Groningen', 12, 3);

COMMIT;

PRAGMA foreign_keys = ON;
