package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

func main() {
	db, _ := sql.Open("sqlite", os.Args[1])
	defer db.Close()

	hdr := func(s string) { fmt.Printf("\n===== %s =====\n", s) }
	list := func(label, q string, args ...any) {
		fmt.Printf("\n  %s\n", label)
		rows, err := db.Query(q, args...)
		if err != nil {
			fmt.Printf("    ERR: %v\n", err)
			return
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		n := 0
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			rows.Scan(ptrs...)
			out := "    "
			for i, c := range cols {
				v := vals[i]
				if b, ok := v.([]byte); ok {
					s := string(b)
					if len(s) > 80 {
						s = s[:80] + "…"
					}
					v = s
				}
				out += fmt.Sprintf("%s=%v ", c, v)
			}
			fmt.Println(out)
			n++
		}
		if n == 0 {
			fmt.Println("    (none)")
		}
	}

	hdr("timetable_entries schema")
	list("", `SELECT name FROM pragma_table_info('timetable_entries')`)

	hdr("Sample entries for a 00:00 Bernina shunt (id=21)")
	list("", `SELECT * FROM timetable_entries WHERE timetable_id = 21 ORDER BY id LIMIT 20`)

	hdr("Distribution: 00:00 durations where the LAST entry has Time2 < start_time")
	list("", `
		WITH dur00 AS (
			SELECT t.id, t.start_time,
			       (SELECT MAX(MAX(COALESCE(e.time1,''), COALESCE(e.time2,''))) FROM timetable_entries e WHERE e.timetable_id = t.id) AS last_time
			FROM timetables t WHERE t.duration = '00:00'
		)
		SELECT
		  SUM(CASE WHEN start_time IS NULL OR start_time = '' THEN 1 ELSE 0 END) AS no_start,
		  SUM(CASE WHEN last_time IS NULL OR last_time = '' THEN 1 ELSE 0 END) AS no_last_time,
		  COUNT(*) AS total
		FROM dur00`)

	hdr("Sample 00:00 services with entries: do entries have time fields at all?")
	list("", `SELECT timetable_id, time1, time2, action FROM timetable_entries WHERE timetable_id IN (21,1,143) ORDER BY timetable_id, id LIMIT 30`)

	hdr("TC coords gap — examine one TC service in detail")
	list("entries for TC service 29020 (PlayerService):", `SELECT * FROM timetable_entries WHERE timetable_id = 29020 ORDER BY id`)
	list("entries for TC service 29055 (Acela Express Introduction WITH coords):", `SELECT * FROM timetable_entries WHERE timetable_id = 29055 ORDER BY id`)
	list("timetable_coordinates row for 29055 (first 200 chars):", `SELECT timetable_id, SUBSTR(coordinates,1,200) FROM timetable_coordinates WHERE timetable_id = 29055`)
}
