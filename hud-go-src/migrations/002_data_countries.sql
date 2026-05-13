-- Migration 002: Data for countries
-- Rows 1 to 10 of 10

PRAGMA foreign_keys = OFF;

BEGIN TRANSACTION;

INSERT INTO [countries] (id, name, code) VALUES (1, 'United Kingdom', 'GB');
INSERT INTO [countries] (id, name, code) VALUES (2, 'Germany', 'DE');
INSERT INTO [countries] (id, name, code) VALUES (3, 'United States', 'US');
INSERT INTO [countries] (id, name, code) VALUES (4, 'France', 'FR');
INSERT INTO [countries] (id, name, code) VALUES (5, 'Austria', 'AT');
INSERT INTO [countries] (id, name, code) VALUES (6, 'Switzerland', 'CH');
INSERT INTO [countries] (id, name, code) VALUES (9, 'Canada', 'CA');
INSERT INTO [countries] (id, name, code) VALUES (10, 'Italy', 'IT');
INSERT INTO [countries] (id, name, code) VALUES (11, 'Czech Republic', 'CZ');
INSERT INTO [countries] (id, name, code) VALUES (12, 'Netherlands', 'NL');

COMMIT;

PRAGMA foreign_keys = ON;
