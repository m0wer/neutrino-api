/*
neutrinod is a standalone REST API server for neutrino, a privacy-preserving
Bitcoin light client using BIP157/BIP158 compact block filters.
*/
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/btcsuite/btclog"
	"github.com/gorilla/mux"

	"github.com/m0wer/neutrino-api/neutrino_server/internal/api"
	"github.com/m0wer/neutrino-api/neutrino_server/internal/auth"
	"github.com/m0wer/neutrino-api/neutrino_server/internal/neutrino"
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
	addPeers := flag.String("addpeer", getEnv("ADD_PEERS", ""), "Comma-separated list of peers to add while still allowing discovery")
	torProxy := flag.String("torproxy", getEnv("TOR_PROXY", ""), "Tor SOCKS5 proxy address (e.g., 127.0.0.1:9050)")
	prefetchFilters := flag.Bool("prefetchfilters", getEnvBool("PREFETCH_FILTERS", false), "Enable background compact filter prefetch (default: disabled to save storage)")
	prefetchWorkers := flag.Int("prefetchworkers", getEnvInt("PREFETCH_WORKERS", 0), "Number of workers for background filter prefetch (0=auto)")
	prefetchStart := flag.Int("prefetchstart", getEnvInt("PREFETCH_START", 0), "Start height for background filter prefetch")
	prefetchLookback := flag.Int("prefetchlookback", getEnvInt("PREFETCH_LOOKBACK", 105120), "When >0 and prefetchstart=0, auto-compute prefetch start as tip minus this many blocks (~2 years default)")
	clearnetInitialSync := flag.Bool("clearnet-initial-sync", getEnvBool("CLEARNET_INITIAL_SYNC", true), "Sync block headers over clearnet before switching to Tor (safe: headers are public data)")
	cfilterCDNAuto := flag.Bool("cfilter-cdn-auto", getEnvBool("CFILTER_CDN_AUTO", true), "Enable automatic compact filter download from block-dn CDN after P2P header sync")
	cfilterCDNURL := flag.String("cfilter-cdn-url", getEnv("CFILTER_CDN_URL", ""), "Override block-dn base URL for compact filter CDN downloads")
	autoSyncWatched := flag.Bool("auto-sync-watched", getEnvBool("AUTO_SYNC_WATCHED", true), "Continuously scan new blocks for watched addresses in the background, keeping the UTXO set up-to-date so /v1/utxos is instant")
	autoSyncIntervalSec := flag.Int("auto-sync-interval", getEnvInt("AUTO_SYNC_INTERVAL_SEC", 30), "Seconds between auto-sync polling passes for new blocks (only used when --auto-sync-watched is enabled)")
	mempoolEnabled := flag.Bool("mempool", getEnvBool("MEMPOOL_ENABLED", true), "Enable watched-only mempool tracking: relay tx invs from peers and track unconfirmed transactions matching watched addresses")
	noAuth := flag.Bool("no-auth", getEnvBool("NO_AUTH", false), "Disable TLS and token authentication (for development/regtest)")
	resetAuth := flag.Bool("reset-auth", false, "Regenerate TLS cert and auth token, clear watched addresses, then exit")
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
		if *clearnetInitialSync {
			logger.Infof("Clearnet initial sync: enabled (headers are public data, safe over clearnet)")
		}
	}

	// Ensure data directory exists
	if err := os.MkdirAll(*dataDir, 0750); err != nil {
		logger.Errorf("Failed to create data directory: %v", err)
		os.Exit(1)
	}

	// Handle --reset-auth: regenerate credentials, clear privacy data, exit.
	if *resetAuth {
		if err := handleResetAuth(*dataDir, logger); err != nil {
			logger.Errorf("Reset auth failed: %v", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	// Set up TLS and auth token (unless --no-auth).
	var tlsCfg *tls.Config
	var authToken string

	if !*noAuth {
		tlsConfig, err := auth.LoadOrGenerateTLS(*dataDir, logger)
		if err != nil {
			logger.Errorf("Failed to set up TLS: %v", err)
			os.Exit(1)
		}
		tlsCfg = tlsConfig.Config
		logger.Infof("TLS certificate: %s", tlsConfig.CertPath)

		authLogger := backend.Logger("AUTH")
		authLogger.SetLevel(level)
		authToken, err = auth.LoadOrGenerateToken(*dataDir, authLogger)
		if err != nil {
			logger.Errorf("Failed to set up auth token: %v", err)
			os.Exit(1)
		}
		logger.Infof("Auth token loaded (clients must present Bearer token)")
	} else {
		logger.Warnf("Authentication disabled (--no-auth). API is unprotected!")
	}

	// Create neutrino node
	nodeConfig := &neutrino.Config{
		Network:             *network,
		DataDir:             *dataDir,
		TorProxy:            *torProxy,
		AddPeers:            *addPeers,
		Version:             version,
		MaxPeers:            8,
		FilterCacheSize:     100 * 1024 * 1024,
		PrefetchFilters:     *prefetchFilters,
		PrefetchWorkers:     *prefetchWorkers,
		PrefetchStart:       int32(*prefetchStart),
		PrefetchLookback:    int32(*prefetchLookback),
		ClearnetInitialSync: *clearnetInitialSync,
		CFilterCDNAuto:      *cfilterCDNAuto,
		CFilterCDNURL:       *cfilterCDNURL,
		AutoSyncWatched:     *autoSyncWatched,
		AutoSyncInterval:    time.Duration(*autoSyncIntervalSec) * time.Second,
		MempoolEnabled:      *mempoolEnabled,
		Logger:              backend,
		LogLevel:            *logLevel,
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
	handler := api.NewHandler(node, apiLogger, version)

	// Set up router
	router := mux.NewRouter()

	// Register auth middleware before API routes (if enabled).
	if authToken != "" {
		authLogger := backend.Logger("AUTH")
		authLogger.SetLevel(level)
		router.Use(auth.Middleware(authToken, authLogger))
	}

	handler.RegisterRoutes(router)

	// Create HTTP(S) server
	server := &http.Server{
		Addr:         *listen,
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	if tlsCfg != nil {
		server.TLSConfig = tlsCfg
	}

	// Start server in background
	go func() {
		if tlsCfg != nil {
			logger.Infof("HTTPS server listening on %s (TLS enabled)", *listen)
			if err := server.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				logger.Errorf("HTTPS server error: %v", err)
			}
		} else {
			logger.Infof("HTTP server listening on %s", *listen)
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Errorf("HTTP server error: %v", err)
			}
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

// handleResetAuth regenerates TLS credentials and auth token, clears
// privacy-sensitive data (watched addresses, UTXOs), and prints the
// new auth token to stdout.
func handleResetAuth(dataDir string, logger btclog.Logger) error {
	logger.Info("Resetting auth credentials...")

	// Remove existing TLS files so they get regenerated.
	for _, name := range []string{auth.TLSCertFilename, auth.TLSKeyFilename, auth.TokenFilename} {
		path := fmt.Sprintf("%s/%s", dataDir, name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove %s: %w", path, err)
		}
	}

	// Generate fresh TLS cert and auth token.
	if _, err := auth.LoadOrGenerateTLS(dataDir, logger); err != nil {
		return fmt.Errorf("failed to generate TLS: %w", err)
	}
	token, err := auth.LoadOrGenerateToken(dataDir, logger)
	if err != nil {
		return fmt.Errorf("failed to generate token: %w", err)
	}

	// Clear watched addresses and UTXOs from the state database.
	storeLogger := logger // reuse same logger for brevity
	store, err := neutrino.OpenStateStore(dataDir, storeLogger)
	if err != nil {
		logger.Warnf("Could not open state store to clear privacy data: %v", err)
	} else {
		if err := store.ClearPrivacyData(); err != nil {
			logger.Warnf("Failed to clear privacy data: %v", err)
		} else {
			logger.Info("Cleared watched addresses and UTXO set")
		}
		store.Close()
	}

	logger.Info("Auth reset complete")
	fmt.Printf("New auth token: %s\n", token)
	fmt.Printf("TLS certificate: %s/%s\n", dataDir, auth.TLSCertFilename)
	return nil
}

// getEnv returns the value of an environment variable or a default value.
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getEnvBool returns a bool env var or a default value.
func getEnvBool(key string, defaultValue bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}

	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return defaultValue
	}
	return parsed
}

// getEnvInt returns an int env var or a default value.
func getEnvInt(key string, defaultValue int) int {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		return defaultValue
	}
	return parsed
}
