package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"hud-go/internal/config"
	"hud-go/internal/database"
	"hud-go/internal/router"
	"hud-go/internal/tsw"
	"hud-go/internal/util"
)

const port = 3000

func main() {
	// Parse CLI flags
	noSubscriptions := false
	for _, arg := range os.Args[1:] {
		if arg == "--no-subscriptions" {
			noSubscriptions = true
		}
	}

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	if noSubscriptions {
		cfg.EnableSubscriptions = false
	}

	// Open database
	db, err := database.Open()
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Create TSW client
	tswClient := tsw.NewClient()
	stopTSW := make(chan struct{})
	if cfg.EnableSubscriptions {
		// Idempotent variant — the connection loop is the only thing that
		// polls subscription data, so the subscription handlers also call
		// EnsureConnectionLoop after Reset / Create to start it on demand
		// when EnableSubscriptions was off at boot.
		tsw.EnsureConnectionLoop(tswClient, cfg, stopTSW)
	}

	// Build router
	r := router.New(db, tswClient)

	// Get network address
	ip := util.GetInternalIP()

	// Create server
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: r,
	}

	// Graceful shutdown
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-done
		fmt.Println("\nShutting down server...")
		close(stopTSW)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	// Print banner
	fmt.Println()
	fmt.Println("========================================")
	fmt.Println("  TSW HUD Project Server (Go)")
	fmt.Println("========================================")
	if noSubscriptions {
		fmt.Println("Subscriptions disabled (via --no-subscriptions flag).")
	}
	fmt.Printf("  Local:   http://127.0.0.1:%d\n", port)
	fmt.Printf("  Network: http://%s:%d\n", ip, port)
	fmt.Println("========================================")
	fmt.Println()

	// Start server
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}

	fmt.Println("Server closed.")
}
