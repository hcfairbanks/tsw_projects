package output

import (
	"encoding/csv"
	"fmt"
	"io"

	"tsw6-timetable/internal/pak/uasset"
)

// WriteCSV writes all timetable data as a flat CSV file.
// Each row is one station stop, with the service headcode and route repeated.
func WriteCSV(w io.Writer, timetables []*uasset.Timetable) error {
	cw := csv.NewWriter(w)

	// Header
	if err := cw.Write([]string{
		"route",
		"headcode",
		"service_name",
		"formation",
		"stop_sequence",
		"station",
		"arrival",
		"departure",
		"platform",
	}); err != nil {
		return err
	}

	for _, tt := range timetables {
		for _, svc := range tt.Services {
			for i, stop := range svc.Stops {
				row := []string{
					tt.Route,
					svc.Headcode,
					svc.ServiceName,
					svc.Formation,
					itoa(i + 1),
					stop.StationName,
					stop.ArrivalTime,
					stop.DepartureTime,
					stop.Platform,
				}
				if err := cw.Write(row); err != nil {
					return err
				}
			}
		}
	}

	cw.Flush()
	return cw.Error()
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}