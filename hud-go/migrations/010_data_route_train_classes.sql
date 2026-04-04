-- Migration 010: Data for route_train_classes
-- Rows 1 to 22 of 22

PRAGMA foreign_keys = OFF;

BEGIN TRANSACTION;

INSERT INTO [route_train_classes] (id, route_id, class_id) VALUES (41, 12, 38);
INSERT INTO [route_train_classes] (id, route_id, class_id) VALUES (42, 12, 39);
INSERT INTO [route_train_classes] (id, route_id, class_id) VALUES (43, 12, 40);
INSERT INTO [route_train_classes] (id, route_id, class_id) VALUES (44, 12, 41);
INSERT INTO [route_train_classes] (id, route_id, class_id) VALUES (140, 25, 75);
INSERT INTO [route_train_classes] (id, route_id, class_id) VALUES (286, 46, 62);
INSERT INTO [route_train_classes] (id, route_id, class_id) VALUES (287, 46, 113);
INSERT INTO [route_train_classes] (id, route_id, class_id) VALUES (344, 55, 123);
INSERT INTO [route_train_classes] (id, route_id, class_id) VALUES (352, 60, 131);
INSERT INTO [route_train_classes] (id, route_id, class_id) VALUES (353, 60, 132);
INSERT INTO [route_train_classes] (id, route_id, class_id) VALUES (372, 67, 124);
INSERT INTO [route_train_classes] (id, route_id, class_id) VALUES (373, 67, 146);
INSERT INTO [route_train_classes] (id, route_id, class_id) VALUES (374, 67, 5);
INSERT INTO [route_train_classes] (id, route_id, class_id) VALUES (375, 162, 5);
INSERT INTO [route_train_classes] (id, route_id, class_id) VALUES (395, 76, 150);
INSERT INTO [route_train_classes] (id, route_id, class_id) VALUES (396, 76, 157);
INSERT INTO [route_train_classes] (id, route_id, class_id) VALUES (405, 81, 91);
INSERT INTO [route_train_classes] (id, route_id, class_id) VALUES (406, 81, 92);
INSERT INTO [route_train_classes] (id, route_id, class_id) VALUES (407, 67, 162);
INSERT INTO [route_train_classes] (id, route_id, class_id) VALUES (408, 67, 81);
INSERT INTO [route_train_classes] (id, route_id, class_id) VALUES (409, 12, 163);
INSERT INTO [route_train_classes] (id, route_id, class_id) VALUES (410, 12, 164);

COMMIT;

PRAGMA foreign_keys = ON;
