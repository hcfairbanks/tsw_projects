package tsw

import (
	"encoding/json"
	"fmt"
	"log"
	"time"
)

// subscriptionEndpoints lists all TSW CommAPI subscription paths to register.
// These must match exactly what the Node.js version uses.
var subscriptionEndpoints = []string{
	"/subscription/TimeOfDay.Data?Subscription=1",
	"/subscription/DriverAid.Data?Subscription=1",
	"/subscription/DriverAid.PlayerInfo?Subscription=1",
	"/subscription/DriverAid.TrackData?Subscription=1",
	"/subscription/CurrentDrivableActor.Function.HUD_GetSpeed?Subscription=1",
	"/subscription/CurrentDrivableActor.Function.HUD_GetDirection?Subscription=1",
	"/subscription/CurrentDrivableActor.Function.HUD_GetPowerHandle?Subscription=1",
	"/subscription/CurrentDrivableActor.Function.HUD_GetIsSlipping?Subscription=1",
	"/subscription/CurrentDrivableActor.Function.HUD_GetBrakeGauge_1?Subscription=1",
	"/subscription/CurrentDrivableActor.Function.HUD_GetBrakeGauge_2?Subscription=1",
	"/subscription/CurrentDrivableActor.Function.HUD_GetAcceleration?Subscription=1",
	"/subscription/CurrentDrivableActor.Function.HUD_GetSpeedControlTarget?Subscription=1",
	"/subscription/CurrentDrivableActor.Function.HUD_GetMaxPermittedSpeed?Subscription=1",
	"/subscription/CurrentDrivableActor.Function.HUD_GetTractiveEffort?Subscription=1",
	"/subscription/CurrentDrivableActor.Function.HUD_GetElectricBrakeHandle?Subscription=1",
	"/subscription/CurrentDrivableActor.Function.HUD_GetLocomotiveBrakeHandle?Subscription=1",
	"/subscription/CurrentDrivableActor.Function.HUD_GetTrainBrakeHandle?Subscription=1",
	"/subscription/CurrentDrivableActor.Function.HUD_GetIsTractionLocked?Subscription=1",
	"/subscription/CurrentFormation/1/Door_PassengerDoor_BR.Function.GetCurrentOutputValue?Subscription=1",
	"/subscription/CurrentFormation/1/Door_PassengerDoor_BL.Function.GetCurrentOutputValue?Subscription=1",
	// CurrentFormation.FormationLength returns the player's live consist
	// length as {Values: {FormationLength: N}}. Feeds the HUD's
	// applyDynamicStopCoords so distance-to-stop tracks the actual stop
	// point for the current consist (after uncouples / formation swaps).
	"/subscription/CurrentFormation.FormationLength?Subscription=1",
	"/subscription/CurrentDrivableActor/PassengerDoor_FR.Function.GetCurrentInputValue?Subscription=1",
	"/subscription/CurrentDrivableActor/PassengerDoor_FL.Function.GetCurrentInputValue?Subscription=1",
	"/subscription/WeatherManager.Data?Subscription=1",
}

// SubscriptionCount returns the number of subscription endpoints.
func SubscriptionCount() int {
	return len(subscriptionEndpoints)
}

// CreateSubscriptions POSTs each subscription endpoint to register them with TSW.
func CreateSubscriptions(client *Client) error {
	log.Println("[TSW] Creating subscriptions...")
	for _, endpoint := range subscriptionEndpoints {
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
