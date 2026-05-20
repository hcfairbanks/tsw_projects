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
	tables := []string{"routes", "timetables", "timetable_entries", "timetable_coordinates", "formations", "formation_vehicles", "train_classes", "sections", "car_stop_signs", "track_markers", "route_coordinates", "route_markers", "locations", "route_locations"}
	for _, t := range tables {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + t).Scan(&n); err != nil {
			fmt.Printf("%-32s ERR %v\n", t, err)
			continue
		}
		fmt.Printf("%-32s %d rows\n", t, n)
	}
	if st, err := os.Stat(os.Args[1]); err == nil {
		fmt.Printf("\nDB file: %.2f GB (%d bytes)\n", float64(st.Size())/1024/1024/1024, st.Size())
	}
	// Routes summary
	fmt.Println("\n=== routes by country ===")
	rows, _ := db.Query(`SELECT c.code, COUNT(*) FROM routes r LEFT JOIN countries c ON c.id=r.country_id GROUP BY c.code ORDER BY 2 DESC`)
	for rows.Next() {
		var code sql.NullString
		var n int
		rows.Scan(&code, &n)
		fmt.Printf("  %-4s  %d\n", code.String, n)
	}
}
