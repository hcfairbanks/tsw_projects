package handler

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"

	"hud-go/internal/util"
)

type AnalysisHandler struct {
	db *sql.DB
}

type ttData struct {
	ID           int      `json:"id"`
	ServiceName  string   `json:"service_name"`
	Bound        *string  `json:"bound"`
	Stops        []string `json:"stops"`
	StopCount    int      `json:"stopCount"`
	FirstStation *string  `json:"firstStation"`
	LastStation  *string  `json:"lastStation"`
	TrainNames   []string `json:"trainNames"`
	Tonnage      *float64 `json:"tonnage"`
	CarCount     *int     `json:"car_count"`
	TrainLength  *float64 `json:"train_length"`
}

func (h *AnalysisHandler) Analyze(w http.ResponseWriter, r *http.Request) {
	routeID, _ := strconv.Atoi(r.URL.Query().Get("route_id"))
	if routeID == 0 {
		routeID, _ = strconv.Atoi(r.URL.Query().Get("routeId"))
	}
	if routeID == 0 {
		util.Error(w, 400, "route_id is required")
		return
	}

	// Get all timetables for route
	rows, err := h.db.Query(`SELECT id, service_name, bound, tonnage, car_count, train_length
		FROM timetables WHERE route_id = ?`, routeID)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	defer rows.Close()

	var timetables []ttData
	for rows.Next() {
		var t ttData
		if err := rows.Scan(&t.ID, &t.ServiceName, &t.Bound, &t.Tonnage, &t.CarCount, &t.TrainLength); err != nil {
			util.Error(w, 500, err.Error())
			return
		}
		timetables = append(timetables, t)
	}

	if len(timetables) == 0 {
		util.JSON(w, 200, map[string]any{"groups": []any{}, "missingCoords": []any{}})
		return
	}

	// Build stop sequences and train names for each timetable
	for i := range timetables {
		tt := &timetables[i]

		// Get stops (STOP AT LOCATION entries)
		eRows, err := h.db.Query(`
			SELECT COALESCE(l.name, '') FROM timetable_entries te
			LEFT JOIN timetable_actions ta ON ta.id = te.action_id
			LEFT JOIN locations l ON l.id = te.location_id
			WHERE te.timetable_id = ? AND ta.name = 'STOP AT LOCATION'
			ORDER BY te.sort_order`, tt.ID)
		if err == nil {
			for eRows.Next() {
				var loc string
				eRows.Scan(&loc)
				loc = strings.TrimSpace(loc)
				if loc != "" {
					tt.Stops = append(tt.Stops, loc)
				}
			}
			eRows.Close()
		}
		if tt.Stops == nil {
			tt.Stops = []string{}
		}
		tt.StopCount = len(tt.Stops)
		if tt.StopCount > 0 {
			tt.FirstStation = &tt.Stops[0]
			tt.LastStation = &tt.Stops[tt.StopCount-1]
		}

		// Get train names
		tRows, err := h.db.Query(`
			SELECT tr.name FROM trains tr
			INNER JOIN timetable_trains ttt ON ttt.train_id = tr.id
			WHERE ttt.timetable_id = ?`, tt.ID)
		if err == nil {
			for tRows.Next() {
				var name string
				tRows.Scan(&name)
				tt.TrainNames = append(tt.TrainNames, name)
			}
			tRows.Close()
		}
		if tt.TrainNames == nil {
			tt.TrainNames = []string{}
		}
	}

	// Group by direction
	groups := groupByDirection(timetables)

	// Build response groups
	result := make([]map[string]any, 0, len(groups))
	for _, group := range groups {
		// Sort by stop count descending
		sortByStopCount(group)
		rec := group[0]
		first := "?"
		last := "?"
		if rec.FirstStation != nil {
			first = *rec.FirstStation
		}
		if rec.LastStation != nil {
			last = *rec.LastStation
		}
		result = append(result, map[string]any{
			"directionLabel": first + " → " + last,
			"recommended":    rec,
			"alternatives":   group[1:],
			"timetableCount": len(group),
		})
	}

	// Sort groups by recommended stop count desc
	for i := 0; i < len(result)-1; i++ {
		for j := i + 1; j < len(result); j++ {
			ri := result[i]["recommended"].(ttData)
			rj := result[j]["recommended"].(ttData)
			if rj.StopCount > ri.StopCount {
				result[i], result[j] = result[j], result[i]
			}
		}
	}

	// Find entries with locations missing lat/lng
	missingCoords := make([]map[string]any, 0)
	for _, tt := range timetables {
		mRows, err := h.db.Query(`
			SELECT COALESCE(l.name, '') FROM timetable_entries te
			LEFT JOIN locations l ON l.id = te.location_id
			WHERE te.timetable_id = ? AND te.location_id IS NOT NULL
			  AND (te.latitude IS NULL OR te.latitude = '' OR te.longitude IS NULL OR te.longitude = '')`, tt.ID)
		if err != nil {
			continue
		}
		var locs []string
		for mRows.Next() {
			var loc string
			mRows.Scan(&loc)
			if loc != "" {
				locs = append(locs, loc)
			}
		}
		mRows.Close()
		if len(locs) > 0 {
			missingCoords = append(missingCoords, map[string]any{
				"timetable_id": tt.ID,
				"service_name": tt.ServiceName,
				"locations":    locs,
			})
		}
	}

	util.JSON(w, 200, map[string]any{"groups": result, "missingCoords": missingCoords})
}

// --- Direction grouping ---

func groupByDirection(data []ttData) [][]ttData {
	withStops := make([]ttData, 0)
	for _, t := range data {
		if t.StopCount > 0 {
			withStops = append(withStops, t)
		}
	}

	assigned := map[int]bool{}
	var groups [][]ttData

	for _, tt := range withStops {
		if assigned[tt.ID] {
			continue
		}
		group := []ttData{tt}
		assigned[tt.ID] = true

		for _, other := range withStops {
			if assigned[other.ID] {
				continue
			}
			if isSameDirection(tt, other) {
				group = append(group, other)
				assigned[other.ID] = true
			}
		}
		groups = append(groups, group)
	}
	return groups
}

func isSameDirection(a, b ttData) bool {
	if a.Bound != nil && b.Bound != nil && *a.Bound != "" && *b.Bound != "" {
		return *a.Bound == *b.Bound
	}

	if a.FirstStation != nil && b.FirstStation != nil && a.LastStation != nil && b.LastStation != nil {
		if *a.FirstStation == *b.FirstStation && *a.LastStation == *b.LastStation {
			return true
		}
		if *a.FirstStation == *b.LastStation && *a.LastStation == *b.FirstStation {
			return false
		}
	}

	if len(a.Stops) > 0 && len(b.Stops) > 0 {
		overlap := lcsLength(a.Stops, b.Stops)
		shorter := len(a.Stops)
		if len(b.Stops) < shorter {
			shorter = len(b.Stops)
		}
		if float64(overlap)/float64(shorter) > 0.5 {
			return true
		}
		reversed := make([]string, len(b.Stops))
		for i, s := range b.Stops {
			reversed[len(b.Stops)-1-i] = s
		}
		revOverlap := lcsLength(a.Stops, reversed)
		if float64(revOverlap)/float64(shorter) > 0.5 {
			return false
		}
	}

	return false
}

func lcsLength(a, b []string) int {
	m, n := len(a), len(b)
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else {
				if dp[i-1][j] > dp[i][j-1] {
					dp[i][j] = dp[i-1][j]
				} else {
					dp[i][j] = dp[i][j-1]
				}
			}
		}
	}
	return dp[m][n]
}

func sortByStopCount(data []ttData) {
	for i := 0; i < len(data)-1; i++ {
		for j := i + 1; j < len(data); j++ {
			if data[j].StopCount > data[i].StopCount {
				data[i], data[j] = data[j], data[i]
			}
		}
	}
}
