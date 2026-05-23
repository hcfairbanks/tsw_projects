package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"hud-go/internal/config"
	"hud-go/internal/tsw"
	"hud-go/internal/util"
)

// SubscriptionHandler handles TSW subscription API endpoints.
type SubscriptionHandler struct {
	client *tsw.Client
}

// GetStatus returns the current subscription connection status.
func (h *SubscriptionHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	hasKey := cfg.ApiKey != "" || tsw.ResolveAPIKeyAvailable(cfg)

	util.Success(w, map[string]any{
		"hasApiKey":            hasKey,
		"apiKeySource":        tsw.APIKeySource(cfg),
		"isConnected":         h.client.IsConnected(),
		"subscriptionsCreated": h.client.IsConnected(),
		"subscriptionCount":   tsw.SubscriptionCount(),
	})
}

// GetData returns the latest subscription data from the TSW API.
func (h *SubscriptionHandler) GetData(w http.ResponseWriter, r *http.Request) {
	if !h.client.IsConnected() {
		data, err := tsw.FetchSubscriptionData(h.client)
		if err != nil {
			util.Error(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		util.Success(w, data)
		return
	}

	util.Success(w, h.client.GetLastData())
}

// Reset deletes existing subscriptions and recreates them. Forces a fresh
// API-key read first — TSW6 rewrites CommAPIKey.txt on game start, and the
// connection loop's periodic poll may not have caught up by the time the
// user hits this button.
//
// Also ensures the connection loop is running — otherwise subscriptions
// are created on the TSW side but nothing polls the data, leaving
// /api/subscription/data empty and breaking the /start auto-detect flow
// when EnableSubscriptions was off at boot and toggled on later.
func (h *SubscriptionHandler) Reset(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	// Honour --no-subscriptions / enableSubscriptions=false in the config.
	// Without this guard, any UI auto-call (start page bootstrap, HUD
	// connect, etc.) restarts the polling loop even when the user asked
	// for it off at boot.
	if !cfg.EnableSubscriptions {
		util.Error(w, http.StatusServiceUnavailable, "subscriptions disabled (enableSubscriptions=false in config, or --no-subscriptions flag)")
		return
	}
	if key := tsw.ReloadAPIKey(h.client, cfg); key == "" {
		util.Error(w, http.StatusServiceUnavailable, "no API key available — start TSW6 once so it generates CommAPIKey.txt, or set apiKey in /settings")
		return
	}
	tsw.DeleteSubscription(h.client)
	if err := tsw.CreateSubscriptions(h.client); err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Idempotent: no-op if the loop is already running.
	tsw.EnsureConnectionLoop(h.client, cfg, nil)
	util.Success(w, map[string]any{"success": true})
}

// TestPath proxies a single read-only GET to the TSW CommAPI so the settings
// UI can validate a custom subscription path (and inspect its Values shape)
// before saving it. Reloads the key first since TSW6 rotates CommAPIKey.txt.
func (h *SubscriptionHandler) TestPath(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		util.Error(w, http.StatusBadRequest, "missing path query parameter")
		return
	}
	if key := tsw.ReloadAPIKey(h.client, config.Get()); key == "" {
		util.Error(w, http.StatusServiceUnavailable, "no API key available — start TSW6 so it generates CommAPIKey.txt")
		return
	}
	raw, err := h.client.Do("GET", "/get/"+path, "")
	if err != nil {
		util.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	var parsed any
	if jsonErr := json.Unmarshal(raw, &parsed); jsonErr != nil {
		util.Success(w, map[string]any{"raw": string(raw)})
		return
	}
	util.Success(w, parsed)
}

// Delete removes all subscriptions. Reload the key first so the DELETE
// requests aren't issued with a stale (or empty) DTGCommKey.
func (h *SubscriptionHandler) Delete(w http.ResponseWriter, r *http.Request) {
	tsw.ReloadAPIKey(h.client, config.Get())
	tsw.DeleteSubscription(h.client)
	util.Success(w, map[string]any{"success": true})
}

// Create registers all subscriptions with the TSW API. Same key-reload +
// loop-start reasoning as Reset.
func (h *SubscriptionHandler) Create(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	if !cfg.EnableSubscriptions {
		util.Error(w, http.StatusServiceUnavailable, "subscriptions disabled (enableSubscriptions=false in config, or --no-subscriptions flag)")
		return
	}
	if key := tsw.ReloadAPIKey(h.client, cfg); key == "" {
		util.Error(w, http.StatusServiceUnavailable, "no API key available — start TSW6 once so it generates CommAPIKey.txt, or set apiKey in /settings")
		return
	}
	if err := tsw.CreateSubscriptions(h.client); err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	tsw.EnsureConnectionLoop(h.client, cfg, nil)
	util.Success(w, map[string]any{"success": true})
}
