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

// Reset deletes existing subscriptions and recreates them.
func (h *SubscriptionHandler) Reset(w http.ResponseWriter, r *http.Request) {
	tsw.DeleteSubscription(h.client)
	if err := tsw.CreateSubscriptions(h.client); err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	util.Success(w, map[string]any{"success": true})
}

// Delete removes all subscriptions.
func (h *SubscriptionHandler) Delete(w http.ResponseWriter, r *http.Request) {
	tsw.DeleteSubscription(h.client)
	util.Success(w, map[string]any{"success": true})
}

// Create registers all subscriptions with the TSW API.
func (h *SubscriptionHandler) Create(w http.ResponseWriter, r *http.Request) {
	if err := tsw.CreateSubscriptions(h.client); err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	util.Success(w, map[string]any{"success": true})
}
