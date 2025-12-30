/*
neutrinod is a standalone REST API server for neutrino, a privacy-preserving
Bitcoin light client using BIP157/BIP158 compact block filters.
*/
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/btcsuite/btclog"
	"github.com/gorilla/mux"

	"github.com/yourusername/neutrino-api/neutrino_server/internal/api"
	"github.com/yourusername/neutrino-api/neutrino_server/internal/neutrino"
)

var (
	// Version is set at build time via ldflags
	version = "dev"
)

func main() {
	// Parse command line flags
	network := flag.String("network", getEnv("NETWORK", "mainnet"), "Bitcoin network (mainnet, testnet, regtest, signet)")
	listen := flag.String("listen", getEnv("LISTEN_ADDR", "0.0.0.0:8334"), "REST API listen address")
	dataDir := flag.String("datadir", getEnv("DATA_DIR", "/data/neutrino"), "Data directory for headers and filters")
	logLevel := flag.String("loglevel", getEnv("LOG_LEVEL", "info"), "Log level (trace, debug, info, warn, error)")
	connectPeers := flag.String("connect", getEnv("CONNECT_PEERS", ""), "Comma-separated list of peers to connect to")
	torProxy := flag.String("torproxy", getEnv("TOR_PROXY", ""), "Tor SOCKS5 proxy address (e.g., 127.0.0.1:9050)")
	showVersion := flag.Bool("version", false, "Show version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("neutrinod %s\n", version)
		os.Exit(0)
	}

	// Set up logging
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("MAIN")
	level, _ := btclog.LevelFromString(*logLevel)
	logger.SetLevel(level)

	logger.Infof("Starting neutrinod %s", version)
	logger.Infof("Network: %s", *network)
	logger.Infof("Listen address: %s", *listen)
	logger.Infof("Data directory: %s", *dataDir)
	if *torProxy != "" {
		logger.Infof("Tor proxy: %s", *torProxy)
	}

	// Ensure data directory exists
	if err := os.MkdirAll(*dataDir, 0750); err != nil {
		logger.Errorf("Failed to create data directory: %v", err)
		os.Exit(1)
	}

	// Create neutrino node
	nodeConfig := &neutrino.Config{
		Network:      *network,
		DataDir:      *dataDir,
		TorProxy:     *torProxy,
		ConnectPeers: *connectPeers,
		MaxPeers:     8,
		Logger:       backend,
		LogLevel:     *logLevel,
	}

	node, err := neutrino.NewNode(nodeConfig)
	if err != nil {
		logger.Errorf("Failed to create neutrino node: %v", err)
		os.Exit(1)
	}

	// Start the node
	if err := node.Start(); err != nil {
		logger.Errorf("Failed to start neutrino node: %v", err)
		os.Exit(1)
	}

	// Create API handler
	apiLogger := backend.Logger("API")
	apiLogger.SetLevel(level)
	handler := api.NewHandler(node, apiLogger)

	// Set up router
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	// Create HTTP server
	server := &http.Server{
		Addr:         *listen,
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start HTTP server in background
	go func() {
		logger.Infof("HTTP server listening on %s", *listen)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Errorf("HTTP server error: %v", err)
		}
	}()

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down...")

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logger.Errorf("HTTP server shutdown error: %v", err)
	}

	if err := node.Stop(); err != nil {
		logger.Errorf("Neutrino node shutdown error: %v", err)
	}

	logger.Info("Shutdown complete")
}

// getEnv returns the value of an environment variable or a default value.
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
