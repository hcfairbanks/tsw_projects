package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"hud-go/internal/util"
)

// StreamHandler handles SSE (Server-Sent Events) streaming of telemetry data.
type StreamHandler struct {
	db *sql.DB
	// GetData returns the current telemetry data to stream to clients.
	GetData func() map[string]any
	// GetRecordingState returns recording state to merge into stream data.
	GetRecordingState func() map[string]any
}

// Handle streams telemetry data to the client via Server-Sent Events.
func (h *StreamHandler) Handle(w http.ResponseWriter, r *http.Request) {
	// Use ResponseController for reliable flushing (works through middleware wrappers)
	rc := http.NewResponseController(w)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	fmt.Fprintf(w, ": keepalive\n\n")
	rc.Flush()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			var data map[string]any
			if h.GetData != nil {
				data = h.GetData()
			}
			if data == nil {
				data = map[string]any{
					"waiting": true,
					"message": "Waiting for TSW connection...",
				}
			}

			// Merge recording state into stream data
			if h.GetRecordingState != nil {
				if recState := h.GetRecordingState(); recState != nil {
					data["recording"] = recState
				}
			}

			jsonBytes, err := json.Marshal(data)
			if err != nil {
				jsonBytes = []byte(`{"error":"failed to marshal telemetry data"}`)
			}
			_, writeErr := fmt.Fprintf(w, "data: %s\n\n", jsonBytes)
			if writeErr != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

// handleStreamRaw is a raw http.HandlerFunc that bypasses middleware wrapping.
// Use this if the chi middleware interferes with SSE flushing.
func handleStreamRaw(h *StreamHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			util.Error(w, http.StatusInternalServerError, "streaming not supported")
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("X-Accel-Buffering", "no")

		fmt.Fprintf(w, ": keepalive\n\n")
		flusher.Flush()

		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				var data map[string]any
				if h.GetData != nil {
					data = h.GetData()
				}
				if data == nil {
					data = map[string]any{
						"waiting": true,
						"message": "Waiting for TSW connection...",
					}
				}

				if h.GetRecordingState != nil {
					if recState := h.GetRecordingState(); recState != nil {
						data["recording"] = recState
					}
				}

				jsonBytes, _ := json.Marshal(data)
				_, writeErr := fmt.Fprintf(w, "data: %s\n\n", jsonBytes)
				if writeErr != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
}
