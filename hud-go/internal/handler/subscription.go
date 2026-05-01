package handler

import (
	"net/http"

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
