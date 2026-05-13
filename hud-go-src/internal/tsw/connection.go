package tsw

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"hud-go/internal/config"
)

// lastLoggedKey lets resolveAPIKey suppress duplicate "Loaded API key…"
// log lines. The connection loop and the various subscription handlers
// all call resolveAPIKey; without this, every poll prints the line even
// when the file hasn't changed. We only emit on transitions
// (empty → key, keyA → keyB, key → empty).
var (
	lastLoggedKeyMu sync.Mutex
	lastLoggedKey   string
)

// connLoopRunning guards against starting more than one connection loop.
// EnsureConnectionLoop / Reset Subscriptions can both fire after boot, and
// without this guard each call would spawn a fresh goroutine that races
// the others over subscriptions + the 100 ms data poll.
var (
	connLoopMu      sync.Mutex
	connLoopRunning bool
)

// EnsureConnectionLoop starts the connection loop iff it isn't already
// running. Used by main.go on boot (when EnableSubscriptions is true) and
// by the subscription handlers (Reset, Create) so the user can flip the
// setting on at runtime and pick up the polling without restarting hud-go.
//
// stopCh may be nil — in that case the loop runs until the process exits,
// which is fine for the runtime-toggle case (the goroutine is already
// running when EnableSubscriptions=true on boot, and there's no per-loop
// stop semantics in this codebase).
func EnsureConnectionLoop(client *Client, cfg *config.Config, stopCh <-chan struct{}) {
	connLoopMu.Lock()
	if connLoopRunning {
		connLoopMu.Unlock()
		return
	}
	connLoopRunning = true
	connLoopMu.Unlock()
	StartConnectionLoop(client, cfg, stopCh)
}

// StartConnectionLoop runs a background goroutine that attempts to connect to the
// TSW CommAPI. On success it initializes subscriptions and begins polling data.
// It retries every 30 seconds on failure. Send on stopCh to shut down.
//
// Prefer EnsureConnectionLoop in new code — it's idempotent.
func StartConnectionLoop(client *Client, cfg *config.Config, stopCh <-chan struct{}) {
	go func() {
		defer func() {
			connLoopMu.Lock()
			connLoopRunning = false
			connLoopMu.Unlock()
		}()
		// Brief delay before first attempt to let TSW API settle
		select {
		case <-time.After(3 * time.Second):
		case <-stopCh:
			return
		}

		// Resolve the API key
		key := resolveAPIKey(cfg)
		if key == "" {
			log.Println("[TSW] No API key found. Connection loop will keep retrying...")
		} else {
			client.SetAPIKey(key)
		}

		for {
			// If we still have no key, try to resolve it again each cycle
			if key == "" {
				key = resolveAPIKey(cfg)
				if key != "" {
					client.SetAPIKey(key)
				}
			}

			if key != "" {
				log.Println("[TSW] Attempting to connect to TSW API...")
				err := client.TestConnection()
				if err != nil && strings.Contains(err.Error(), "NoSuchSubscription") {
					log.Println("[TSW] No existing subscription, creating fresh...")
					client.setConnected(true)
					err = nil
				}
				if err == nil {
					log.Println("[TSW] Connection successful!")
					initializeAndPoll(client, stopCh)
					// If polling exits (e.g. connection lost), fall through to retry
					log.Println("[TSW] Connection lost. Will retry...")
					client.setConnected(false)
				} else {
					log.Printf("[TSW] Connection failed: %v", err)
				}
			}

			log.Println("[TSW] Retrying in 30 seconds...")
			select {
			case <-time.After(30 * time.Second):
			case <-stopCh:
				return
			}
		}
	}()
}

// initializeAndPoll deletes old subscriptions, creates new ones, then polls
// subscription data every 100ms until an error or stop signal.
func initializeAndPoll(client *Client, stopCh <-chan struct{}) {
	// Delete old subscriptions
	DeleteSubscription(client)

	// Brief pause before creating new ones
	select {
	case <-time.After(500 * time.Millisecond):
	case <-stopCh:
		return
	}

	// Create subscriptions
	_ = CreateSubscriptions(client)
	log.Println("[TSW] Subscriptions initialized - streaming ready")

	// Poll subscription data every 100ms
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	consecutiveErrors := 0
	for {
		select {
		case <-ticker.C:
			raw, err := FetchSubscriptionData(client)
			if err != nil {
				consecutiveErrors++
				if consecutiveErrors <= 3 {
					// Transient error — skip this tick
					continue
				}
				if consecutiveErrors <= 10 {
					// Subscription likely dropped — try to recreate
					log.Printf("[TSW] Subscription dropped, recreating... (%d errors)", consecutiveErrors)
					DeleteSubscription(client)
					time.Sleep(500 * time.Millisecond)
					_ = CreateSubscriptions(client)
					consecutiveErrors = 0
					continue
				}
				// Too many consecutive errors — connection lost
				log.Printf("[TSW] Data fetch error (giving up): %v", err)
				return
			}
			consecutiveErrors = 0
			parsed := ParseTelemetry(raw)
			client.setLastData(parsed)
		case <-stopCh:
			return
		}
	}
}

// resolveAPIKey determines the API key from config or the CommAPIKey.txt file.
func resolveAPIKey(cfg *config.Config) string {
	// If config has an explicit apiKey, use it
	if cfg.ApiKey != "" {
		return strings.TrimSpace(cfg.ApiKey)
	}

	// Otherwise, read from the CommAPIKey.txt file
	keyPath := commAPIKeyPath(cfg)
	data, err := os.ReadFile(keyPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[TSW] Error reading CommAPIKey.txt at %s: %v", keyPath, err)
		}
		return ""
	}

	key := strings.TrimSpace(string(data))
	logKeyTransition(key, keyPath)
	return key
}

// logKeyTransition prints a one-line log only when the resolved key
// changes — first load, rotation by TSW6 on game start, or a switch back
// to no-key. Idempotent reads (the common case under the polling loop)
// stay silent.
func logKeyTransition(newKey, source string) {
	lastLoggedKeyMu.Lock()
	defer lastLoggedKeyMu.Unlock()
	if newKey == lastLoggedKey {
		return
	}
	switch {
	case newKey != "" && lastLoggedKey == "":
		log.Printf("[TSW] Loaded API key from %s", source)
	case newKey != "" && lastLoggedKey != "":
		log.Printf("[TSW] API key changed (re-read from %s)", source)
	case newKey == "" && lastLoggedKey != "":
		log.Printf("[TSW] API key cleared (file at %s now empty/unreadable)", source)
	}
	lastLoggedKey = newKey
}

// commAPIKeyPath returns the path to the CommAPIKey.txt based on TSW version and config overrides.
func commAPIKeyPath(cfg *config.Config) string {
	// Check for explicit path overrides in config
	switch cfg.TswVersion {
	case "tsw5":
		if cfg.Tsw5KeyPath != "" {
			return cfg.Tsw5KeyPath
		}
	default: // tsw6 or unset
		if cfg.Tsw6KeyPath != "" {
			return cfg.Tsw6KeyPath
		}
	}

	// Default paths under USERPROFILE/Documents/My Games/
	userProfile := os.Getenv("USERPROFILE")
	if userProfile == "" {
		userProfile = os.Getenv("HOME")
	}

	var gameName string
	switch cfg.TswVersion {
	case "tsw5":
		gameName = "TrainSimWorld5"
	default:
		gameName = "TrainSimWorld6"
	}

	return filepath.Join(userProfile, "Documents", "My Games", gameName, "Saved", "Config", "CommAPIKey.txt")
}

// ResolveAPIKeyAvailable checks whether an API key can be resolved from config or file.
func ResolveAPIKeyAvailable(cfg *config.Config) bool {
	return resolveAPIKey(cfg) != ""
}

// ReloadAPIKey re-reads CommAPIKey.txt (or config.ApiKey) and pushes the
// current value onto the client. Used by handlers that issue API calls
// right away (Reset Subscriptions, Create Subscriptions) so they don't
// race the connection loop's polling — TSW6 regenerates the file on game
// start, and the file's contents can change between hud-go startup and
// the user clicking the button.
//
// Returns the resolved key (or "") so callers can short-circuit when no
// key is available rather than blasting the API with a stale one.
func ReloadAPIKey(client *Client, cfg *config.Config) string {
	key := resolveAPIKey(cfg)
	if client != nil {
		client.SetAPIKey(key)
	}
	return key
}

// APIKeySource returns a human-readable description of where the API key comes from.
func APIKeySource(cfg *config.Config) string {
	if cfg.ApiKey != "" {
		return fmt.Sprintf("Configuration File")
	}
	return fmt.Sprintf("TSW CommAPIKey.txt (%s)", commAPIKeyPath(cfg))
}
