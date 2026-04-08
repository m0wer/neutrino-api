/*
Package api provides the REST API for the neutrino server.
*/
package api

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btclog"
	"github.com/gorilla/mux"

	"github.com/m0wer/neutrino-api/neutrino_server/internal/neutrino"
)

// NodeInterface defines the interface for neutrino node operations.
type NodeInterface interface {
	GetStatus() neutrino.Status
	GetBlockHeader(height int32) (*wire.BlockHeader, error)
	GetBlockHash(height int32) (*chainhash.Hash, error)
	BroadcastTransaction(tx *wire.MsgTx) error
	GetUTXOs(addresses []string) ([]neutrino.UTXO, error)
	GetUTXO(txid string, vout uint32, address string, startHeight int32) (*neutrino.UTXOSpendReport, error)
	WatchAddress(address string) error
	Rescan(startHeight int32, addresses []string) error
	IsRescanInProgress() bool
	RescanStatus() neutrino.RescanStatus
}

// Handler provides REST API endpoints for the neutrino node.
type Handler struct {
	node    NodeInterface
	logger  btclog.Logger
	version string
}

// NewHandler creates a new API handler.
func NewHandler(node NodeInterface, logger btclog.Logger, version ...string) *Handler {
	resolvedVersion := ""
	if len(version) > 0 {
		resolvedVersion = version[0]
	}

	return &Handler{
		node:    node,
		logger:  logger,
		version: resolvedVersion,
	}
}

// RegisterRoutes registers all API routes.
func (h *Handler) RegisterRoutes(r *mux.Router) {
	// Add request logging middleware.
	r.Use(h.loggingMiddleware)

	// Status
	r.HandleFunc("/v1/status", h.handleGetStatus).Methods("GET")
	r.HandleFunc("/v1/version", h.handleGetVersion).Methods("GET")

	// Block queries
	r.HandleFunc("/v1/block/{height}/header", h.handleGetBlockHeader).Methods("GET")
	r.HandleFunc("/v1/block/{height}/filter_header", h.handleGetFilterHeader).Methods("GET")

	// Transaction operations
	r.HandleFunc("/v1/tx/{txid}", h.handleGetTransaction).Methods("GET")
	r.HandleFunc("/v1/tx/broadcast", h.handleBroadcastTransaction).Methods("POST")

	// UTXO operations
	r.HandleFunc("/v1/utxos", h.handleGetUTXOs).Methods("POST")
	r.HandleFunc("/v1/utxo/{txid}/{vout}", h.handleGetUTXO).Methods("GET")

	// Watch operations
	r.HandleFunc("/v1/watch/address", h.handleWatchAddress).Methods("POST")
	r.HandleFunc("/v1/watch/outpoint", h.handleWatchOutpoint).Methods("POST")

	// Rescan
	r.HandleFunc("/v1/rescan", h.handleRescan).Methods("POST")
	r.HandleFunc("/v1/rescan/status", h.handleGetRescanStatus).Methods("GET")

	// Peers
	r.HandleFunc("/v1/peers", h.handleGetPeers).Methods("GET")
}

// statusRecorder wraps http.ResponseWriter to capture the status code.
type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (rec *statusRecorder) WriteHeader(code int) {
	rec.statusCode = code
	rec.ResponseWriter.WriteHeader(code)
}

// loggingMiddleware logs every HTTP request with method, path, status, and duration.
func (h *Handler) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rec, r)
		duration := time.Since(start)

		// Use Warn level for errors (4xx/5xx), Info for everything else.
		if rec.statusCode >= 400 {
			h.logger.Warnf("%s %s -> %d (%s)", r.Method, r.URL.Path, rec.statusCode, duration.Round(time.Millisecond))
		} else {
			h.logger.Infof("%s %s -> %d (%s)", r.Method, r.URL.Path, rec.statusCode, duration.Round(time.Millisecond))
		}
	})
}

// Response helpers

func (h *Handler) jsonResponse(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	if version := h.effectiveVersion(); version != "" {
		w.Header().Set("X-Neutrino-Version", version)
	}
	json.NewEncoder(w).Encode(data)
}

func (h *Handler) effectiveVersion() string {
	if h.version != "" {
		return h.version
	}

	status := h.node.GetStatus()
	return status.Version
}

func (h *Handler) errorResponse(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// Status endpoint
func (h *Handler) handleGetStatus(w http.ResponseWriter, r *http.Request) {
	status := h.node.GetStatus()
	if status.Version == "" {
		status.Version = h.effectiveVersion()
	}
	h.jsonResponse(w, status)
}

// Version endpoint.
func (h *Handler) handleGetVersion(w http.ResponseWriter, r *http.Request) {
	h.jsonResponse(w, map[string]any{"version": h.effectiveVersion()})
}

// Block header endpoint
func (h *Handler) handleGetBlockHeader(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	heightStr := vars["height"]

	height, err := strconv.ParseInt(heightStr, 10, 32)
	if err != nil {
		h.errorResponse(w, http.StatusBadRequest, "invalid height")
		return
	}

	header, err := h.node.GetBlockHeader(int32(height))
	if err != nil {
		h.errorResponse(w, http.StatusNotFound, err.Error())
		return
	}

	blockHash, _ := h.node.GetBlockHash(int32(height))

	h.jsonResponse(w, map[string]any{
		"hash":        blockHash.String(),
		"height":      height,
		"timestamp":   header.Timestamp.Unix(),
		"version":     header.Version,
		"prev_block":  header.PrevBlock.String(),
		"merkle_root": header.MerkleRoot.String(),
		"bits":        header.Bits,
		"nonce":       header.Nonce,
	})
}

// Filter header endpoint
func (h *Handler) handleGetFilterHeader(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	heightStr := vars["height"]

	height, err := strconv.ParseInt(heightStr, 10, 32)
	if err != nil {
		h.errorResponse(w, http.StatusBadRequest, "invalid height")
		return
	}

	// Filter headers would come from the filter header store
	// This is a placeholder - full implementation needed
	h.jsonResponse(w, map[string]any{
		"height":        height,
		"filter_header": "",
	})
}

// Transaction endpoint
func (h *Handler) handleGetTransaction(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	txid := vars["txid"]

	// Neutrino doesn't store full transactions by default
	// This would require fetching from a peer or having received it
	h.errorResponse(w, http.StatusNotImplemented, "transaction lookup requires full block download")
	_ = txid
}

// Broadcast transaction endpoint
func (h *Handler) handleBroadcastTransaction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TxHex string `json:"tx_hex"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.errorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}

	txBytes, err := hex.DecodeString(req.TxHex)
	if err != nil {
		h.errorResponse(w, http.StatusBadRequest, "invalid transaction hex")
		return
	}

	var tx wire.MsgTx
	if err := tx.Deserialize(bytes.NewReader(txBytes)); err != nil {
		h.errorResponse(w, http.StatusBadRequest, "failed to deserialize transaction")
		return
	}

	if err := h.node.BroadcastTransaction(&tx); err != nil {
		h.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}

	txid := tx.TxHash().String()
	h.logger.Infof("Broadcast transaction: %s", txid)

	h.jsonResponse(w, map[string]string{
		"txid": txid,
	})
}

// UTXOs endpoint
func (h *Handler) handleGetUTXOs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Addresses []string `json:"addresses"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.errorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}

	utxos, err := h.node.GetUTXOs(req.Addresses)
	if err != nil {
		h.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.jsonResponse(w, map[string]any{
		"utxos": utxos,
	})
}

// UTXO lookup endpoint
func (h *Handler) handleGetUTXO(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	txid := vars["txid"]
	voutStr := vars["vout"]

	vout, err := strconv.ParseUint(voutStr, 10, 32)
	if err != nil {
		h.errorResponse(w, http.StatusBadRequest, "invalid vout")
		return
	}

	// Required address query parameter (needed for compact block filter matching)
	address := r.URL.Query().Get("address")
	if address == "" {
		h.errorResponse(w, http.StatusBadRequest, "address parameter is required")
		return
	}

	// Optional start_height query parameter
	startHeight := int32(0)
	if sh := r.URL.Query().Get("start_height"); sh != "" {
		if parsed, err := strconv.ParseInt(sh, 10, 32); err == nil {
			startHeight = int32(parsed)
		}
	}

	report, err := h.node.GetUTXO(txid, uint32(vout), address, startHeight)
	if err != nil {
		// Check for typed errors to return appropriate status codes
		var notFoundErr *neutrino.NotFoundError
		var badRequestErr *neutrino.BadRequestError

		if errors.As(err, &notFoundErr) {
			h.errorResponse(w, http.StatusNotFound, err.Error())
		} else if errors.As(err, &badRequestErr) {
			h.errorResponse(w, http.StatusBadRequest, err.Error())
		} else {
			h.errorResponse(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	h.jsonResponse(w, report)
}

// Watch address endpoint
func (h *Handler) handleWatchAddress(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Address string `json:"address"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.errorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := h.node.WatchAddress(req.Address); err != nil {
		h.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	h.jsonResponse(w, map[string]string{
		"status": "ok",
	})
}

// Watch outpoint endpoint
func (h *Handler) handleWatchOutpoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TxID string `json:"txid"`
		Vout uint32 `json:"vout"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.errorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Store outpoint for watching
	// Full implementation would track this and notify on spend
	h.jsonResponse(w, map[string]string{
		"status": "ok",
	})
}

// Rescan endpoint
func (h *Handler) handleRescan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StartHeight int32    `json:"start_height"`
		Addresses   []string `json:"addresses"`
		Outpoints   []struct {
			TxID string `json:"txid"`
			Vout uint32 `json:"vout"`
		} `json:"outpoints"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.errorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}

	h.logger.Infof("Rescan requested: start_height=%d, addresses=%d, outpoints=%d",
		req.StartHeight, len(req.Addresses), len(req.Outpoints))

	// Start rescan in background goroutine to not block HTTP response
	go func() {
		if err := h.node.Rescan(req.StartHeight, req.Addresses); err != nil {
			h.logger.Errorf("Rescan failed: %v", err)
		}
	}()

	h.jsonResponse(w, map[string]string{
		"status": "started",
	})
}

// Rescan status endpoint
func (h *Handler) handleGetRescanStatus(w http.ResponseWriter, r *http.Request) {
	nodeStatus := h.node.GetStatus()
	status := h.node.RescanStatus()
	h.jsonResponse(w, map[string]any{
		"in_progress":       status.InProgress,
		"last_started":      status.LastStarted,
		"last_finished":     status.LastFinished,
		"last_start_height": status.LastStartHeight,
		"last_scanned_tip":  status.LastScannedTip,
		"last_error":        status.LastError,
		"watched_addresses": nodeStatus.WatchedAddresses,
		"server_version":    h.effectiveVersion(),
	})
}

// Peers endpoint
func (h *Handler) handleGetPeers(w http.ResponseWriter, r *http.Request) {
	status := h.node.GetStatus()

	h.jsonResponse(w, map[string]any{
		"peers": []any{}, // Would list connected peers
		"count": status.Peers,
	})
}
