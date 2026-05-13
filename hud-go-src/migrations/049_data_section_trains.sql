-- Migration 049: Data for section_trains
-- Rows 1 to 22 of 22

PRAGMA foreign_keys = OFF;

BEGIN TRANSACTION;

INSERT INTO [section_trains] (id, section_id, train_id) VALUES (1, 7, 194);
INSERT INTO [section_trains] (id, section_id, train_id) VALUES (2, 7, 195);
INSERT INTO [section_trains] (id, section_id, train_id) VALUES (3, 8, 150);
INSERT INTO [section_trains] (id, section_id, train_id) VALUES (4, 1, 40);
INSERT INTO [section_trains] (id, section_id, train_id) VALUES (5, 1, 38);
INSERT INTO [section_trains] (id, section_id, train_id) VALUES (6, 1, 39);
INSERT INTO [section_trains] (id, section_id, train_id) VALUES (7, 1, 41);
INSERT INTO [section_trains] (id, section_id, train_id) VALUES (8, 1, 164);
INSERT INTO [section_trains] (id, section_id, train_id) VALUES (9, 1, 165);
INSERT INTO [section_trains] (id, section_id, train_id) VALUES (10, 2, 40);
INSERT INTO [section_trains] (id, section_id, train_id) VALUES (11, 2, 41);
INSERT INTO [section_trains] (id, section_id, train_id) VALUES (12, 2, 39);
INSERT INTO [section_trains] (id, section_id, train_id) VALUES (13, 2, 38);
INSERT INTO [section_trains] (id, section_id, train_id) VALUES (14, 5, 181);
INSERT INTO [section_trains] (id, section_id, train_id) VALUES (15, 5, 179);
INSERT INTO [section_trains] (id, section_id, train_id) VALUES (16, 5, 180);
INSERT INTO [section_trains] (id, section_id, train_id) VALUES (17, 6, 157);
INSERT INTO [section_trains] (id, section_id, train_id) VALUES (18, 6, 178);
INSERT INTO [section_trains] (id, section_id, train_id) VALUES (19, 6, 146);
INSERT INTO [section_trains] (id, section_id, train_id) VALUES (20, 6, 180);
INSERT INTO [section_trains] (id, section_id, train_id) VALUES (21, 6, 179);
INSERT INTO [section_trains] (id, section_id, train_id) VALUES (22, 7, 275);

COMMIT;

PRAGMA foreign_keys = ON;
