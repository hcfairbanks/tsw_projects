package tsw

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"hud-go/internal/config"
)

// subscriptionEndpoints returns the TSW CommAPI subscription URLs to register,
// built from the enabled entries in config.ApiCalls. The list is config-driven
// so users can toggle builtins and add custom calls (see config/apicalls.go);
// the builtin defaults reproduce the set the app has always subscribed to.
func subscriptionEndpoints() []string {
	paths := config.EnabledSubscriptionPaths()
	urls := make([]string, 0, len(paths))
	for _, p := range paths {
		urls = append(urls, "/subscription/"+p+"?Subscription=1")
	}
	return urls
}

// SubscriptionCount returns the number of enabled subscription endpoints.
func SubscriptionCount() int {
	return len(config.EnabledSubscriptionPaths())
}

// CreateSubscriptions POSTs each subscription endpoint to register them with TSW.
func CreateSubscriptions(client *Client) error {
	log.Println("[TSW] Creating subscriptions...")
	for _, endpoint := range subscriptionEndpoints() {
		_, err := client.Do("POST", endpoint, "")
		if err != nil {
			log.Printf("[TSW] Failed to create subscription %s: %v", endpoint, err)
			// Continue creating remaining subscriptions
		}
		time.Sleep(250 * time.Millisecond)
	}
	log.Println("[TSW] All subscriptions created")
	return nil
}

// DeleteSubscription sends a DELETE to remove all subscriptions.
// Ignores errors since subscriptions may not exist yet.
func DeleteSubscription(client *Client) {
	log.Println("[TSW] Deleting old subscriptions...")
	_, err := client.Do("DELETE", "/subscription/?Subscription=1", "")
	if err != nil {
		// This is normal if no subscriptions exist yet
		log.Printf("[TSW] Delete returned: %v (this is OK)", err)
	} else {
		log.Println("[TSW] Old subscriptions deleted")
	}
}

// FetchSubscriptionData GETs the current subscription data and parses it as JSON.
func FetchSubscriptionData(client *Client) (map[string]any, error) {
	data, err := client.Do("GET", "/subscription/?Subscription=1", "")
	if err != nil {
		return nil, fmt.Errorf("fetching subscription data: %w", err)
	}

	if len(data) == 0 {
		return map[string]any{}, nil
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		// Return the raw data as a string if it's not valid JSON
		return map[string]any{"raw": string(data)}, nil
	}

	return result, nil
}
