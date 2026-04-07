package handler

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"math"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"hud-go/internal/config"
	"hud-go/internal/util"
)

type OCRHandler struct {
	db *sql.DB
}

// findTesseract returns the path to the tesseract executable.
// Checks bundled tesseract/ directory first, then PATH.
func findTesseract() (string, string) {
	// Check bundled tesseract next to the executable
	appDir := config.AppDir()
	bundled := filepath.Join(appDir, "tesseract", "tesseract.exe")
	if _, err := os.Stat(bundled); err == nil {
		tessdata := filepath.Join(appDir, "tesseract", "tessdata")
		return bundled, tessdata
	}
	// Fall back to PATH
	if p, err := exec.LookPath("tesseract"); err == nil {
		return p, ""
	}
	return "", ""
}

// GetStatus checks if tesseract is available.
func (h *OCRHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	tess, _ := findTesseract()
	util.JSON(w, 200, map[string]bool{"available": tess != ""})
}

// ---------- response types ----------

type ocrEntry struct {
	Action    string `json:"action"`
	ActionID  *int   `json:"action_id"`
	Details   string `json:"details"`
	Location  string `json:"location"`
	StructureNumber string `json:"structure_number"`
	Time1     string `json:"time1"`
	Time2     string `json:"time2"`
	Latitude  string `json:"latitude"`
	Longitude string `json:"longitude"`
	SortOrder int    `json:"sort_order"`
}

type rawTextEntry struct {
	File string `json:"file"`
	Text string `json:"text"`
}

type ocrResponse struct {
	ServiceName  string         `json:"service_name"`
	RouteName    string         `json:"route_name,omitempty"`
	StartTime    *string        `json:"start_time,omitempty"`
	Duration     *string        `json:"duration,omitempty"`
	Tonnage      *float64       `json:"tonnage,omitempty"`
	CarCount     *int           `json:"car_count,omitempty"`
	TrainLength  *float64       `json:"train_length,omitempty"`
	LevelInfoRaw string         `json:"level_info_raw,omitempty"`
	Entries      []ocrEntry     `json:"entries"`
	RawTexts     []rawTextEntry `json:"rawTexts"`
}

// ---------- Extract handler ----------

func (h *OCRHandler) Extract(w http.ResponseWriter, r *http.Request) {
	// Check tesseract availability
	tessPath, tessData := findTesseract()
	if tessPath == "" {
		util.Error(w, http.StatusServiceUnavailable, "Tesseract not found. Place tesseract/ folder next to the exe, or install tesseract-ocr on PATH.")
		return
	}

	// Parse multipart form (max 100 MB)
	if err := r.ParseMultipartForm(100 << 20); err != nil {
		util.Error(w, http.StatusBadRequest, "Failed to parse multipart form: "+err.Error())
		return
	}

	// Collect image files from the multipart form
	type imageFile struct {
		filename string
		header   *multipart.FileHeader
	}

	imageExts := map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".bmp": true}
	var images []imageFile

	for _, fileHeaders := range r.MultipartForm.File {
		for _, fh := range fileHeaders {
			ext := strings.ToLower(filepath.Ext(fh.Filename))
			if imageExts[ext] {
				images = append(images, imageFile{filename: fh.Filename, header: fh})
			}
		}
	}

	if len(images) == 0 {
		util.Error(w, http.StatusBadRequest, "No valid image files uploaded")
		return
	}

	// Sort by filename
	sort.Slice(images, func(i, j int) bool {
		return images[i].filename < images[j].filename
	})

	filenames := make([]string, len(images))
	for i, img := range images {
		filenames[i] = img.filename
	}

	// Determine image roles from filenames
	fnameMap := map[string]int{}
	for i, fn := range filenames {
		fnameMap[fn] = i
	}

	hasServiceNameImage := false
	if _, ok := fnameMap["1b_service_name.png"]; ok {
		hasServiceNameImage = true
	}
	hasRouteImage := false
	if _, ok := fnameMap["0_route.png"]; ok {
		hasRouteImage = true
	}

	routeIdx := -1
	if hasRouteImage {
		routeIdx = fnameMap["0_route.png"]
	}

	serviceIdx := -1
	if idx, ok := fnameMap["1_service.png"]; ok {
		serviceIdx = idx
	} else if hasRouteImage && routeIdx == 0 {
		serviceIdx = -1
	} else {
		serviceIdx = 0
	}

	serviceNameIdx := -1
	if hasServiceNameImage {
		serviceNameIdx = fnameMap["1b_service_name.png"]
	}

	// Level info field mapping by filename
	levelFieldMap := map[string]string{
		"1c_start_time.png": "startTime",
		"1d_duration.png":   "duration",
		"2a_tonnage.png":    "tonnage",
		"2b_car_count.png":  "carCount",
		"2c_car_length.png": "trainLength",
	}
	timeFields := map[string]bool{"startTime": true, "duration": true}

	levelIndices := map[int]string{}  // index -> field name
	timetableIndices := []int{}

	// Assign roles based on filenames
	for i := range images {
		if i == routeIdx || i == serviceIdx || i == serviceNameIdx {
			continue
		}
		if field, ok := levelFieldMap[filenames[i]]; ok {
			levelIndices[i] = field
		} else {
			timetableIndices = append(timetableIndices, i)
		}
	}

	// Positional fallback when no level info matched by filename
	if len(levelIndices) == 0 && !hasServiceNameImage {
		n := len(images)
		if n >= 8 && routeIdx < 0 {
			routeIdx = 0
			serviceIdx = 1
			timetableIndices = nil
			levelIndices[2] = "startTime"
			levelIndices[3] = "duration"
			levelIndices[4] = "tonnage"
			levelIndices[5] = "carCount"
			levelIndices[6] = "trainLength"
			for i := 7; i < n; i++ {
				timetableIndices = append(timetableIndices, i)
			}
		} else if n >= 5 {
			timetableIndices = nil
			levelIndices[1] = "tonnage"
			levelIndices[2] = "carCount"
			levelIndices[3] = "trainLength"
			for i := 4; i < n; i++ {
				timetableIndices = append(timetableIndices, i)
			}
		}
	}

	// Create temp dir for tesseract work
	tmpDir, err := os.MkdirTemp("", "hud-ocr-*")
	if err != nil {
		util.Error(w, http.StatusInternalServerError, "Failed to create temp dir: "+err.Error())
		return
	}
	defer os.RemoveAll(tmpDir)

	// Save all images to temp files
	tmpPaths := make([]string, len(images))
	for i, img := range images {
		src, err := img.header.Open()
		if err != nil {
			util.Error(w, http.StatusInternalServerError, "Failed to open uploaded file: "+err.Error())
			return
		}
		tmpPath := filepath.Join(tmpDir, fmt.Sprintf("img_%03d%s", i, filepath.Ext(img.filename)))
		dst, err := os.Create(tmpPath)
		if err != nil {
			src.Close()
			util.Error(w, http.StatusInternalServerError, "Failed to create temp file: "+err.Error())
			return
		}
		if _, err := io.Copy(dst, src); err != nil {
			src.Close()
			dst.Close()
			util.Error(w, http.StatusInternalServerError, "Failed to write temp file: "+err.Error())
			return
		}
		src.Close()
		dst.Close()
		tmpPaths[i] = tmpPath
	}

	log.Printf("Processing %d images for OCR...", len(images))

	// OCR each image using tesseract CLI
	ocrTexts := make([]string, len(images))
	for i, tmpPath := range tmpPaths {
		text, err := runTesseract(tessPath, tessData, tmpPath, tmpDir, i)
		if err != nil {
			log.Printf("Tesseract error on image %d (%s): %v", i, filenames[i], err)
			ocrTexts[i] = ""
		} else {
			ocrTexts[i] = text
		}
	}

	// Process results according to roles
	var serviceName string
	var routeName string
	var allRows []parsedRow
	levelInfo := map[string]interface{}{
		"startTime":   nil,
		"duration":    nil,
		"tonnage":     nil,
		"carCount":    nil,
		"trainLength": nil,
	}
	var levelInfoRaw string
	rawTexts := make([]rawTextEntry, len(images))

	for i := range images {
		text := ocrTexts[i]
		label := filenames[i]

		if i == routeIdx {
			routeName = firstNonEmptyLine(text)
			rawTexts[i] = rawTextEntry{File: label, Text: text}
		} else if i == serviceNameIdx {
			nameOnly := firstNonEmptyLine(text)
			serviceName = nameOnly
			rawTexts[i] = rawTextEntry{File: label, Text: text}
		} else if i == serviceIdx {
			if hasServiceNameImage {
				// Service image used only for time info in new method
				rawTexts[i] = rawTextEntry{File: label, Text: text}
			} else {
				parsed := parseTrainTimetable(text)
				if parsed.serviceName != "" {
					serviceName = parsed.serviceName
				}
				allRows = append(allRows, parsed.rows...)
				rawTexts[i] = rawTextEntry{File: label, Text: text}
			}
		} else if field, ok := levelIndices[i]; ok {
			if timeFields[field] {
				v := parseTimeValue(text)
				levelInfo[field] = v
			} else {
				levelInfo[field] = parseSingleLevelValue(text, field)
			}
			if text == "" {
				text = "(none)"
			}
			levelInfoRaw += fmt.Sprintf("%s: %s ", field, text)
			rawTexts[i] = rawTextEntry{File: label, Text: ocrTexts[i]}
		} else {
			// Timetable image
			parsed := parseTrainTimetable(text)
			if serviceName == "" && parsed.serviceName != "" {
				serviceName = parsed.serviceName
			}
			allRows = append(allRows, parsed.rows...)
			rawTexts[i] = rawTextEntry{File: label, Text: text}
		}
	}

	// Deduplicate rows
	uniqueRows := deduplicateRows(allRows)

	// Build action lookup from database
	actionLookup := h.loadActionLookup()

	// Build response entries
	entries := make([]ocrEntry, len(uniqueRows))
	for i, row := range uniqueRows {
		actionName := normalizeActionName(row.action, actionLookup)
		var actionID *int
		if id, ok := actionLookup[actionName]; ok {
			actionID = &id
		}
		entries[i] = ocrEntry{
			Action:    actionName,
			ActionID:  actionID,
			Details:   cleanOcrValue(row.details),
			Location:  cleanOcrValue(row.location),
			StructureNumber: cleanOcrValue(row.platform),
			Time1:     cleanOcrValue(row.time1),
			Time2:     cleanOcrValue(row.time2),
			Latitude:  "",
			Longitude: "",
			SortOrder: i,
		}
	}

	if serviceName == "" {
		serviceName = "Extracted Timetable"
	}

	resp := ocrResponse{
		ServiceName: serviceName,
		Entries:     entries,
		RawTexts:    rawTexts,
	}

	if routeName != "" {
		resp.RouteName = routeName
	}

	hasLevelInfo := len(levelIndices) > 0
	if hasLevelInfo {
		if v, ok := levelInfo["startTime"]; ok && v != nil {
			s := v.(string)
			resp.StartTime = &s
		}
		if v, ok := levelInfo["duration"]; ok && v != nil {
			s := v.(string)
			resp.Duration = &s
		}
		if v, ok := levelInfo["tonnage"]; ok && v != nil {
			f := v.(float64)
			resp.Tonnage = &f
		}
		if v, ok := levelInfo["carCount"]; ok && v != nil {
			n := v.(int)
			resp.CarCount = &n
		}
		if v, ok := levelInfo["trainLength"]; ok && v != nil {
			f := v.(float64)
			resp.TrainLength = &f
		}
		resp.LevelInfoRaw = strings.TrimSpace(levelInfoRaw)
	}

	util.JSON(w, 200, resp)
}

// ---------- Tesseract CLI ----------

func runTesseract(tessPath, tessData, inputPath, tmpDir string, idx int) (string, error) {
	outputBase := filepath.Join(tmpDir, fmt.Sprintf("out_%03d", idx))
	outputFile := outputBase + ".txt"

	cmd := exec.Command(tessPath, inputPath, outputBase,
		"-c", `tessedit_char_whitelist=ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789:+-()& [].#`,
		"-c", "preserve_interword_spaces=1",
		"--psm", "6",
	)
	if tessData != "" {
		cmd.Env = append(os.Environ(), "TESSDATA_PREFIX="+tessData)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tesseract failed: %v, output: %s", err, string(out))
	}

	data, err := os.ReadFile(outputFile)
	if err != nil {
		return "", fmt.Errorf("failed to read tesseract output: %v", err)
	}

	return strings.TrimSpace(string(data)), nil
}

// ---------- Timetable text parsing ----------

var (
	reStrictTime  = regexp.MustCompile(`^[+-]?\d{1,2}:\d{2}:\d{2}$`)
	reLenientTime = regexp.MustCompile(`^[+-]?\d{1,2}:[^\s]{2,3}:\d{2}$`)
	reInlineTime  = regexp.MustCompile(`([+-]?\d{1,2}:\d{2}:\d{2})`)
	reLenientInl  = regexp.MustCompile(`([+-]?\d{1,2}:[^\s]{2,3}:\d{2})`)
	rePlatform    = regexp.MustCompile(`(?i)(.+?)\s+(?:Platform|Track)\s+(\d+)`)
	reTimeTrail   = regexp.MustCompile(`\s*-\s*\d{1,2}:\d{2}:\d{2}.*$`)
	reMultiSpace  = regexp.MustCompile(`\s{2,}`)
)

type parsedRow struct {
	action   string
	details  string
	location string
	platform string
	time1    string
	time2    string
}

type parseResult struct {
	serviceName string
	rows        []parsedRow
}

func parseTrainTimetable(text string) parseResult {
	lines := strings.Split(text, "\n")
	var rows []parsedRow
	var serviceName string
	var pendingTime string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		upper := strings.ToUpper(trimmed)
		isActionLine := strings.Contains(upper, "WAIT FOR SERVICE") ||
			(strings.HasPrefix(upper, "WAIT") && !strings.Contains(upper, "WAIT FOR SERVICE")) ||
			strings.Contains(upper, "STOP AT LOCATION") ||
			strings.Contains(upper, "LOAD PASSENGERS") ||
			strings.Contains(upper, "UNLOAD PASSENGERS") ||
			strings.Contains(upper, "GO VIA LOCATION") ||
			strings.Contains(upper, "UNCOUPLE VEHICLES") ||
			strings.Contains(upper, "COUPLE TO FORMATION")

		// Check for orphaned time lines
		if !isActionLine {
			if reStrictTime.MatchString(trimmed) {
				clean := strings.NewReplacer("+", "", "-", "").Replace(trimmed)
				parts := strings.Split(clean, ":")
				if len(parts) == 3 {
					pendingTime = padLeft(parts[0], 2, '0') + ":" + parts[1] + ":" + parts[2]
				}
				continue
			}
			if reLenientTime.MatchString(trimmed) {
				cleaned := strings.NewReplacer("+", "", "-", "").Replace(trimmed)
				cleaned = regexp.MustCompile(`[^0-9:]`).ReplaceAllString(cleaned, "")
				parts := strings.Split(cleaned, ":")
				if len(parts) == 3 {
					pendingTime = padLeft(parts[0], 2, '0') + ":" + padLeft(parts[1], 2, '0') + ":" + parts[2]
				}
				continue
			}
		}

		// Service name: first non-action line when we haven't found one yet
		if !isActionLine && serviceName == "" {
			serviceName = reMultiSpace.ReplaceAllString(trimmed, " ")
			continue
		}

		if !isActionLine {
			// Non-action continuation line — skip
			continue
		}

		// Extract times from the line
		times := reInlineTime.FindAllString(trimmed, -1)
		var malformedInlineTime string
		if len(times) == 0 {
			if m := reLenientInl.FindString(trimmed); m != "" {
				cleaned := strings.NewReplacer("+", "", "-", "").Replace(m)
				cleaned = regexp.MustCompile(`[^0-9:]`).ReplaceAllString(cleaned, "")
				parts := strings.Split(cleaned, ":")
				if len(parts) == 3 {
					malformedInlineTime = padLeft(parts[0], 2, '0') + ":" + padLeft(parts[1], 2, '0') + ":" + parts[2]
				}
			}
		}

		cleanTimes := make([]string, 0, len(times))
		for _, t := range times {
			cleanTimes = append(cleanTimes, normalizeTime(t))
		}
		uniqueTimes := uniqueStrings(cleanTimes)

		var time1 string
		if len(uniqueTimes) >= 1 {
			time1 = uniqueTimes[0]
		}
		// time2 is extracted but not used per the Node.js logic
		// (each action only references time1; WAIT FOR SERVICE puts time1 into the time2 field)
		_ = uniqueTimes

		var action, details, location, platform string

		switch {
		case strings.Contains(upper, "WAIT FOR SERVICE"):
			action = "WAIT FOR SERVICE"
			afterAction := strings.TrimSpace(removeFirst(trimmed, "WAIT FOR SERVICE"))
			// Extract description: everything up to time
			re := regexp.MustCompile(`WAIT FOR SERVICE\s+(.+?)(?:\s+[-+]?\d|$)`)
			if m := re.FindStringSubmatch(trimmed); len(m) > 1 {
				details = strings.TrimSpace(m[1])
			} else {
				details = afterAction
			}
			rows = append(rows, parsedRow{action: action, details: details, time2: time1})
			pendingTime = ""

		case strings.HasPrefix(upper, "WAIT") && !strings.Contains(upper, "WAIT FOR SERVICE"):
			action = "WAIT"
			afterAction := regexp.MustCompile(`(?i)^WAIT\s+`).ReplaceAllString(trimmed, "")
			parts := reMultiSpace.Split(afterAction, -1)
			if len(parts) > 0 && parts[0] != "" {
				details = strings.TrimSpace(parts[0])
			} else {
				details = strings.TrimSpace(afterAction)
			}
			waitTime := time1
			if waitTime == "" {
				waitTime = malformedInlineTime
			}
			if waitTime == "" {
				waitTime = pendingTime
			}
			rows = append(rows, parsedRow{action: action, details: details, time1: waitTime})
			pendingTime = ""

		case strings.Contains(upper, "UNLOAD PASSENGERS"):
			action = "UNLOAD PASSENGERS"
			rows = append(rows, parsedRow{action: action, time1: time1})

		case strings.Contains(upper, "LOAD PASSENGERS"):
			action = "LOAD PASSENGERS"
			rows = append(rows, parsedRow{action: action, time1: time1})

		case strings.Contains(upper, "STOP AT LOCATION"):
			action = "STOP AT LOCATION"
			afterAction := strings.TrimSpace(removeFirst(trimmed, "STOP AT LOCATION"))
			parts := reMultiSpace.Split(afterAction, -1)
			if len(parts) > 0 && parts[0] != "" {
				details = strings.TrimSpace(parts[0])
			} else {
				details = afterAction
			}
			if m := rePlatform.FindStringSubmatch(details); len(m) > 2 {
				location = strings.TrimSpace(m[1])
				platform = m[2]
			} else {
				location = strings.TrimSpace(reTimeTrail.ReplaceAllString(details, ""))
			}
			rows = append(rows, parsedRow{action: action, details: details, location: location, platform: platform, time1: time1})

		case strings.Contains(upper, "GO VIA LOCATION"):
			action = "GO VIA LOCATION"
			afterAction := strings.TrimSpace(removeFirst(trimmed, "GO VIA LOCATION"))
			parts := reMultiSpace.Split(afterAction, -1)
			if len(parts) > 0 && parts[0] != "" {
				details = strings.TrimSpace(parts[0])
			} else {
				details = afterAction
			}
			location = strings.TrimSpace(reTimeTrail.ReplaceAllString(details, ""))
			rows = append(rows, parsedRow{action: action, details: details, location: location, time1: time1})

		case strings.Contains(upper, "UNCOUPLE VEHICLES"):
			action = "UNCOUPLE VEHICLES"
			afterAction := strings.TrimSpace(removeFirst(trimmed, "UNCOUPLE VEHICLES"))
			parts := reMultiSpace.Split(afterAction, -1)
			if len(parts) > 0 && parts[0] != "" {
				details = strings.TrimSpace(parts[0])
			} else {
				details = afterAction
			}
			rows = append(rows, parsedRow{action: action, details: details, time1: time1})

		case strings.Contains(upper, "COUPLE TO FORMATION"):
			action = "COUPLE TO FORMATION"
			afterAction := strings.TrimSpace(removeFirst(trimmed, "COUPLE TO FORMATION"))
			parts := reMultiSpace.Split(afterAction, -1)
			if len(parts) > 0 && parts[0] != "" {
				details = strings.TrimSpace(parts[0])
			} else {
				details = afterAction
			}
			rows = append(rows, parsedRow{action: action, details: details, time1: time1})
		}

		// Clear pending time after any non-WAIT action
		if isActionLine && action != "WAIT" {
			pendingTime = ""
		}
	}

	return parseResult{serviceName: serviceName, rows: rows}
}

// ---------- Level info parsing ----------

func parseSingleLevelValue(text, field string) interface{} {
	if text == "" {
		return nil
	}
	cleaned := regexp.MustCompile(`[^0-9.]`).ReplaceAllString(text, "")
	if cleaned == "" {
		return nil
	}

	if field == "carCount" {
		v, err := strconv.Atoi(cleaned)
		if err != nil {
			return nil
		}
		return v
	}
	// tonnage and trainLength: fix missing decimal
	return fixDecimal(cleaned)
}

func parseTimeValue(text string) interface{} {
	if text == "" {
		return nil
	}
	cleaned := regexp.MustCompile(`[^0-9:]`).ReplaceAllString(strings.TrimSpace(text), "")
	if cleaned == "" {
		return nil
	}
	// Already has colon
	reColon := regexp.MustCompile(`(\d{1,2}):(\d{2})`)
	if m := reColon.FindStringSubmatch(cleaned); len(m) > 2 {
		return padLeft(m[1], 2, '0') + ":" + m[2]
	}
	// No colon: "0457" -> "04:57"
	digits := regexp.MustCompile(`[^0-9]`).ReplaceAllString(cleaned, "")
	if len(digits) >= 3 {
		padded := padLeft(digits, 4, '0')
		return padded[:2] + ":" + padded[2:4]
	}
	return nil
}

func fixDecimal(numStr string) float64 {
	if strings.Contains(numStr, ".") {
		v, _ := strconv.ParseFloat(numStr, 64)
		return v
	}
	if len(numStr) >= 3 {
		v, _ := strconv.ParseFloat(numStr[:len(numStr)-1]+"."+numStr[len(numStr)-1:], 64)
		return math.Round(v*10) / 10
	}
	v, _ := strconv.ParseFloat(numStr, 64)
	return v
}

// ---------- Deduplication ----------

func deduplicateRows(rows []parsedRow) []parsedRow {
	seen := map[string]bool{}
	var result []parsedRow
	for _, row := range rows {
		t1 := strings.TrimSpace(row.time1)
		t2 := strings.TrimSpace(row.time2)
		action := strings.TrimSpace(row.action)
		location := strings.TrimSpace(row.location)
		details := strings.TrimSpace(row.details)

		var key string
		if t1 != "" || t2 != "" {
			key = action + "|" + t1 + "|" + t2
		} else {
			key = action + "|" + location + "|" + details
		}

		if !seen[key] {
			seen[key] = true
			result = append(result, row)
		} else {
			log.Printf("OCR: Skipping duplicate entry: %s at %s", action, firstNonEmpty(t1, t2, location, details))
		}
	}
	log.Printf("OCR: Deduplicated rows: %d -> %d", len(rows), len(result))
	return result
}

// ---------- Action normalization ----------

func (h *OCRHandler) loadActionLookup() map[string]int {
	lookup := map[string]int{}
	rows, err := h.db.Query("SELECT id, name FROM timetable_actions ORDER BY id")
	if err != nil {
		log.Printf("Failed to load actions: %v", err)
		return lookup
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var name string
		if err := rows.Scan(&id, &name); err == nil {
			lookup[name] = id
		}
	}
	return lookup
}

func normalizeActionName(action string, lookup map[string]int) string {
	upper := strings.ToUpper(strings.TrimSpace(action))
	if upper == "" {
		return ""
	}
	if _, ok := lookup[upper]; ok {
		return upper
	}
	return upper
}

// ---------- Helpers ----------

func cleanOcrValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "-" {
		return ""
	}
	return trimmed
}

func firstNonEmptyLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		t = reMultiSpace.ReplaceAllString(t, " ")
		if t != "" {
			return t
		}
	}
	return ""
}

func normalizeTime(t string) string {
	clean := strings.NewReplacer("+", "", "-", "").Replace(t)
	parts := strings.Split(clean, ":")
	if len(parts) == 3 {
		return padLeft(parts[0], 2, '0') + ":" + parts[1] + ":" + parts[2]
	}
	return clean
}

func removeFirst(s, sub string) string {
	// Case-insensitive removal of first occurrence
	upper := strings.ToUpper(s)
	idx := strings.Index(upper, strings.ToUpper(sub))
	if idx < 0 {
		return s
	}
	return s[:idx] + s[idx+len(sub):]
}

func padLeft(s string, length int, pad byte) string {
	for len(s) < length {
		s = string(pad) + s
	}
	return s
}

func uniqueStrings(ss []string) []string {
	seen := map[string]bool{}
	var result []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
