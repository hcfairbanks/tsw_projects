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
	rows, _ := db.Query(`
		SELECT tc.id, tc.name, tc.rail_vehicle_class, tc.thumbnail_path
		FROM train_classes tc
		WHERE tc.type_description IS NOT NULL
		  AND TRIM(tc.type_description) <> ''
		  AND (tc.vehicle_category IS NULL OR tc.vehicle_category <> 'FreightWagon')
		  AND tc.is_drivable = 1
		ORDER BY tc.name COLLATE NOCASE`)
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id int
		var name, rvc, thumb sql.NullString
		rows.Scan(&id, &name, &rvc, &thumb)
		fmt.Printf("%-3d  %-35s rvc=%-22s thumb=%s\n", id, name.String, rvc.String, thumb.String)
		count++
	}
	fmt.Printf("\nTOTAL DISPLAYED: %d train_classes\n", count)
}
