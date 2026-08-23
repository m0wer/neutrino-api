package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/wire/v2"
	"github.com/btcsuite/btclog"
	"github.com/gorilla/mux"

	"github.com/m0wer/neutrino-api/neutrino_server/internal/neutrino"
)

// mockNode implements NodeInterface for testing
type mockNode struct {
	getUTXOs     func(addresses []string) ([]neutrino.UTXO, error)
	getUTXO      func(txid string, vout uint32, address string, startHeight int32) (*neutrino.UTXOSpendReport, error)
	mempoolUTXOs func(addresses []string) []neutrino.MempoolUTXO
	mempoolSpend func(txid string, vout uint32) (neutrino.MempoolSpend, bool)
	mempoolTx    func(txid string) (*wire.MsgTx, bool)
	mempoolStats func() neutrino.MempoolStats
	getStatus    func() neutrino.Status
	txHistory    func(sinceHeight int32) (neutrino.TxHistoryResponse, error)
	startRescan  func(startHeight int32, addresses []string, force bool) error
}

func (m *mockNode) GetStatus() neutrino.Status {
	if m.getStatus != nil {
		return m.getStatus()
	}
	return neutrino.Status{
		Synced:           true,
		BlockHeight:      8543,
		FilterHeight:     8543,
		Peers:            1,
		Version:          testVersion,
		WatchedAddresses: 3,
	}
}

func (m *mockNode) GetBlockHeader(height int32) (*wire.BlockHeader, error) {
	return nil, nil
}

func (m *mockNode) GetBlockHash(height int32) (*chainhash.Hash, error) {
	return nil, nil
}

func (m *mockNode) BroadcastTransaction(tx *wire.MsgTx) error {
	return nil
}

func (m *mockNode) GetUTXOs(addresses []string) ([]neutrino.UTXO, error) {
	if m.getUTXOs != nil {
		return m.getUTXOs(addresses)
	}
	return []neutrino.UTXO{}, nil
}

func (m *mockNode) GetUTXO(txid string, vout uint32, address string, startHeight int32) (*neutrino.UTXOSpendReport, error) {
	if m.getUTXO != nil {
		return m.getUTXO(txid, vout, address, startHeight)
	}
	// Mock response for a spent UTXO
	if txid == "f4184fc596403b9d638783cf57adfe4c75c605f6356fbc91338530e9831e9e16" && vout == 0 {
		return &neutrino.UTXOSpendReport{
			Unspent:        false,
			SpendingTxID:   "ea44e97271691990157559d0bdd9959e02790c34db6c006d779e82fa5aee708e",
			SpendingInput:  0,
			SpendingHeight: 91880,
		}, nil
	}
	// Mock response for an unspent UTXO
	return &neutrino.UTXOSpendReport{
		Unspent:      true,
		Value:        100000000,
		ScriptPubKey: "76a914...",
	}, nil
}

func (m *mockNode) WatchAddress(address string) error {
	return nil
}

func (m *mockNode) StartRescan(startHeight int32, addresses []string, force bool) error {
	if m.startRescan != nil {
		return m.startRescan(startHeight, addresses, force)
	}
	return nil
}

func (m *mockNode) IsRescanInProgress() bool {
	return false
}

func (m *mockNode) RescanStatus() neutrino.RescanStatus {
	return neutrino.RescanStatus{}
}

func (m *mockNode) GetMempoolUTXOs(addresses []string) []neutrino.MempoolUTXO {
	if m.mempoolUTXOs != nil {
		return m.mempoolUTXOs(addresses)
	}
	return nil
}

func (m *mockNode) GetMempoolSpend(txid string, vout uint32) (neutrino.MempoolSpend, bool) {
	if m.mempoolSpend != nil {
		return m.mempoolSpend(txid, vout)
	}
	return neutrino.MempoolSpend{}, false
}

func (m *mockNode) GetMempoolTx(txid string) (*wire.MsgTx, bool) {
	if m.mempoolTx != nil {
		return m.mempoolTx(txid)
	}
	return nil, false
}

func (m *mockNode) MempoolStats() neutrino.MempoolStats {
	if m.mempoolStats != nil {
		return m.mempoolStats()
	}
	return neutrino.MempoolStats{}
}

func (m *mockNode) GetTxHistory(sinceHeight int32) (neutrino.TxHistoryResponse, error) {
	if m.txHistory != nil {
		return m.txHistory(sinceHeight)
	}
	return neutrino.TxHistoryResponse{Transactions: []neutrino.TxHistoryRecord{}, Cursor: sinceHeight}, nil
}

const testVersion = "v0.10.1-test"

func TestHandleGetStatus(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	handler := NewHandler(&mockNode{}, logger, testVersion)

	router := mux.NewRouter()
	router.HandleFunc("/v1/status", handler.handleGetStatus).Methods("GET")

	req, err := http.NewRequest("GET", "/v1/status", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	var response neutrino.Status
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("could not decode response: %v", err)
	}

	if !response.Synced {
		t.Errorf("expected synced=true, got %v", response.Synced)
	}

	if response.BlockHeight != 8543 {
		t.Errorf("expected block_height=8543, got %v", response.BlockHeight)
	}

	if response.FilterHeight != 8543 {
		t.Errorf("expected filter_height=8543, got %v", response.FilterHeight)
	}

	if response.Peers != 1 {
		t.Errorf("expected peers=1, got %v", response.Peers)
	}

	if response.Version != testVersion {
		t.Errorf("expected version=%s, got %v", testVersion, response.Version)
	}

	if response.WatchedAddresses != 3 {
		t.Errorf("expected watched_addresses=3, got %v", response.WatchedAddresses)
	}

	if got := rr.Header().Get("X-Neutrino-Version"); got != testVersion {
		t.Errorf("expected X-Neutrino-Version=%s, got %v", testVersion, got)
	}
}

func TestHandleGetVersion(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	handler := NewHandler(&mockNode{}, logger, testVersion)

	router := mux.NewRouter()
	router.HandleFunc("/v1/version", handler.handleGetVersion).Methods("GET")

	req, err := http.NewRequest("GET", "/v1/version", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	var response map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("could not decode response: %v", err)
	}

	if response["version"] != testVersion {
		t.Errorf("expected version=%s, got %v", testVersion, response["version"])
	}

	if got := rr.Header().Get("X-Neutrino-Version"); got != testVersion {
		t.Errorf("expected X-Neutrino-Version=%s, got %v", testVersion, got)
	}
}

func TestHandleBroadcastTransaction_InvalidJSON(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	handler := NewHandler(&mockNode{}, logger)

	router := mux.NewRouter()
	router.HandleFunc("/v1/tx/broadcast", handler.handleBroadcastTransaction).Methods("POST")

	req, err := http.NewRequest("POST", "/v1/tx/broadcast", bytes.NewBufferString("invalid json"))
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusBadRequest {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusBadRequest)
	}

	var response map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("could not decode response: %v", err)
	}

	if response["error"] != "invalid request body" {
		t.Errorf("unexpected error message: %v", response["error"])
	}
}

func TestHandleBroadcastTransaction_InvalidHex(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	handler := NewHandler(&mockNode{}, logger)

	router := mux.NewRouter()
	router.HandleFunc("/v1/tx/broadcast", handler.handleBroadcastTransaction).Methods("POST")

	body := map[string]string{"tx_hex": "not_hex"}
	jsonBody, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", "/v1/tx/broadcast", bytes.NewBuffer(jsonBody))
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusBadRequest {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusBadRequest)
	}

	var response map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("could not decode response: %v", err)
	}

	if response["error"] != "invalid transaction hex" {
		t.Errorf("unexpected error message: %v", response["error"])
	}
}

func TestHandleGetBlockHeader_InvalidHeight(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	handler := NewHandler(&mockNode{}, logger)

	router := mux.NewRouter()
	router.HandleFunc("/v1/block/{height}/header", handler.handleGetBlockHeader).Methods("GET")

	req, err := http.NewRequest("GET", "/v1/block/invalid/header", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusBadRequest {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusBadRequest)
	}

	var response map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("could not decode response: %v", err)
	}

	if response["error"] != "invalid height" {
		t.Errorf("unexpected error message: %v", response["error"])
	}
}

func TestJSONResponse(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	handler := NewHandler(&mockNode{}, logger)

	rr := httptest.NewRecorder()
	data := map[string]string{"test": "value"}

	handler.jsonResponse(rr, data)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	contentType := rr.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("handler returned wrong content type: got %v want %v", contentType, "application/json")
	}
}

func TestErrorResponse(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	handler := NewHandler(&mockNode{}, logger)

	rr := httptest.NewRecorder()

	handler.errorResponse(rr, http.StatusBadRequest, "test error")

	if status := rr.Code; status != http.StatusBadRequest {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusBadRequest)
	}

	var response map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("could not decode response: %v", err)
	}

	if response["error"] != "test error" {
		t.Errorf("unexpected error message: %v", response["error"])
	}
}

func TestHandleRescan_Success(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")
	called := false

	handler := NewHandler(&mockNode{
		startRescan: func(startHeight int32, addresses []string, force bool) error {
			if startHeight != 100 {
				t.Errorf("expected start height 100, got %d", startHeight)
			}
			if force {
				t.Error("expected force=false for a plain rescan request")
			}
			called = true
			return nil
		},
	}, logger)

	router := mux.NewRouter()
	router.HandleFunc("/v1/rescan", handler.handleRescan).Methods("POST")

	reqBody := map[string]any{
		"start_height": 100,
		"addresses":    []string{"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"},
	}
	jsonBody, _ := json.Marshal(reqBody)

	req, err := http.NewRequest("POST", "/v1/rescan", bytes.NewBuffer(jsonBody))
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	var response map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("could not decode response: %v", err)
	}

	if response["status"] != "started" {
		t.Errorf("expected status 'started', got %v", response["status"])
	}

	if !called {
		t.Fatal("rescan admission was not dispatched synchronously")
	}
}

func TestHandleRescan_Force(t *testing.T) {
	called := false
	handler := NewHandler(&mockNode{
		startRescan: func(startHeight int32, addresses []string, force bool) error {
			if startHeight != 0 {
				t.Errorf("expected start height 0, got %d", startHeight)
			}
			if !force {
				t.Error("expected force=true")
			}
			called = true
			return nil
		},
	}, newTestLogger())
	router := mux.NewRouter()
	router.HandleFunc("/v1/rescan", handler.handleRescan).Methods("POST")

	reqBody := map[string]any{
		"start_height": 0,
		"addresses":    []string{"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"},
		"force":        true,
	}
	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/rescan", bytes.NewReader(jsonBody))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}
	if !called {
		t.Fatal("forced rescan admission was not dispatched synchronously")
	}
}

func TestHandleRescan_Busy(t *testing.T) {
	handler := NewHandler(&mockNode{
		startRescan: func(startHeight int32, addresses []string, force bool) error {
			return neutrino.ErrRescanBusy
		},
	}, newTestLogger())
	router := mux.NewRouter()
	router.HandleFunc("/v1/rescan", handler.handleRescan).Methods("POST")

	reqBody := map[string]any{
		"start_height": 0,
		"addresses":    []string{"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"},
	}
	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/rescan", bytes.NewReader(jsonBody))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d", rr.Code)
	}
}

func TestHandleRescan_InvalidJSON(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	handler := NewHandler(&mockNode{}, logger)

	router := mux.NewRouter()
	router.HandleFunc("/v1/rescan", handler.handleRescan).Methods("POST")

	req, err := http.NewRequest("POST", "/v1/rescan", bytes.NewBufferString("invalid json"))
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusBadRequest {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusBadRequest)
	}

	var response map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("could not decode response: %v", err)
	}

	if response["error"] != "invalid request body" {
		t.Errorf("unexpected error message: %v", response["error"])
	}
}

func TestHandleGetUTXOs_Success(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	handler := NewHandler(&mockNode{}, logger)

	router := mux.NewRouter()
	router.HandleFunc("/v1/utxos", handler.handleGetUTXOs).Methods("POST")

	reqBody := map[string]any{
		"addresses": []string{"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"},
	}
	jsonBody, _ := json.Marshal(reqBody)

	req, err := http.NewRequest("POST", "/v1/utxos", bytes.NewBuffer(jsonBody))
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	var response map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("could not decode response: %v", err)
	}

	if _, ok := response["utxos"]; !ok {
		t.Error("expected 'utxos' field in response")
	}
}

func TestHandleWatchAddress_Success(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	handler := NewHandler(&mockNode{}, logger)

	router := mux.NewRouter()
	router.HandleFunc("/v1/watch/address", handler.handleWatchAddress).Methods("POST")

	reqBody := map[string]any{
		"address": "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa",
	}
	jsonBody, _ := json.Marshal(reqBody)

	req, err := http.NewRequest("POST", "/v1/watch/address", bytes.NewBuffer(jsonBody))
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	var response map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("could not decode response: %v", err)
	}

	if response["status"] != "ok" {
		t.Errorf("expected status 'ok', got %v", response["status"])
	}
}

func TestHandleGetUTXO_Success(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	handler := NewHandler(&mockNode{}, logger)

	router := mux.NewRouter()
	router.HandleFunc("/v1/utxo/{txid}/{vout}", handler.handleGetUTXO).Methods("GET")

	// Test unspent UTXO
	req, err := http.NewRequest("GET", "/v1/utxo/abcd1234/0?address=bc1qtest&start_height=100", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	var response neutrino.UTXOSpendReport
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("could not decode response: %v", err)
	}

	if !response.Unspent {
		t.Error("expected unspent=true")
	}

	if response.Value != 100000000 {
		t.Errorf("expected value=100000000, got %v", response.Value)
	}
}

func TestHandleGetUTXO_Spent(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	handler := NewHandler(&mockNode{}, logger)

	router := mux.NewRouter()
	router.HandleFunc("/v1/utxo/{txid}/{vout}", handler.handleGetUTXO).Methods("GET")

	// Test spent UTXO (Satoshi to Hal Finney transaction)
	req, err := http.NewRequest("GET", "/v1/utxo/f4184fc596403b9d638783cf57adfe4c75c605f6356fbc91338530e9831e9e16/0?address=1Q2TWHE3GMdB6BZKafqwxXtWAWgFt5Jvm3&start_height=150", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	var response neutrino.UTXOSpendReport
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("could not decode response: %v", err)
	}

	if response.Unspent {
		t.Error("expected unspent=false")
	}

	if response.SpendingHeight != 91880 {
		t.Errorf("expected spending_height=91880, got %v", response.SpendingHeight)
	}
}

func TestHandleGetUTXO_InvalidVout(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	handler := NewHandler(&mockNode{}, logger)

	router := mux.NewRouter()
	router.HandleFunc("/v1/utxo/{txid}/{vout}", handler.handleGetUTXO).Methods("GET")

	req, err := http.NewRequest("GET", "/v1/utxo/abcd1234/invalid", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusBadRequest {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusBadRequest)
	}

	var response map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("could not decode response: %v", err)
	}

	if response["error"] != "invalid vout" {
		t.Errorf("unexpected error message: %v", response["error"])
	}
}

func TestHandleGetUTXO_MissingAddress(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	handler := NewHandler(&mockNode{}, logger)

	router := mux.NewRouter()
	router.HandleFunc("/v1/utxo/{txid}/{vout}", handler.handleGetUTXO).Methods("GET")

	// Request without address parameter
	req, err := http.NewRequest("GET", "/v1/utxo/abcd1234/0?start_height=100", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusBadRequest {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusBadRequest)
	}

	var response map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("could not decode response: %v", err)
	}

	if response["error"] != "address parameter is required" {
		t.Errorf("unexpected error message: %v", response["error"])
	}
}

func TestHandleGetRescanStatus_NotInProgress(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	handler := NewHandler(&mockNode{}, logger)

	router := mux.NewRouter()
	router.HandleFunc("/v1/rescan/status", handler.handleGetRescanStatus).Methods("GET")

	req, err := http.NewRequest("GET", "/v1/rescan/status", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	var response map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("could not decode response: %v", err)
	}

	inProgress, ok := response["in_progress"].(bool)
	if !ok {
		t.Fatalf("expected boolean in_progress, got %T", response["in_progress"])
	}

	if inProgress {
		t.Error("expected in_progress=false")
	}

	if _, ok := response["last_started"]; !ok {
		t.Error("expected last_started in response")
	}
	if _, ok := response["last_finished"]; !ok {
		t.Error("expected last_finished in response")
	}
	if _, ok := response["last_start_height"]; !ok {
		t.Error("expected last_start_height in response")
	}
	if _, ok := response["last_scanned_tip"]; !ok {
		t.Error("expected last_scanned_tip in response")
	}
	if supported, ok := response["force_rescan_supported"].(bool); !ok || !supported {
		t.Errorf("expected force_rescan_supported=true, got %v", response["force_rescan_supported"])
	}
	if watched, ok := response["watched_addresses"].(float64); !ok || int(watched) != 3 {
		t.Errorf("expected watched_addresses=3, got %v", response["watched_addresses"])
	}
	if version, ok := response["server_version"].(string); !ok || version != testVersion {
		t.Errorf("expected server_version=%s, got %v", testVersion, response["server_version"])
	}
}

func TestLoggingMiddleware_Success(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	handler := NewHandler(&mockNode{}, logger)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req, err := http.NewRequest("GET", "/v1/status", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	// Verify the response is valid JSON (middleware should not interfere)
	var response neutrino.Status
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("could not decode response: %v", err)
	}
}

func TestLoggingMiddleware_Error(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	handler := NewHandler(&mockNode{}, logger)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	// Invalid body should trigger 400
	req, err := http.NewRequest("POST", "/v1/rescan", bytes.NewBufferString("invalid json"))
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusBadRequest {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusBadRequest)
	}
}

// --- Mempool integration tests ---

func newTestLogger() btclog.Logger {
	return btclog.NewBackend(os.Stdout).Logger("TEST")
}

func postUTXOs(t *testing.T, h *Handler, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	router := mux.NewRouter()
	router.HandleFunc("/v1/utxos", h.handleGetUTXOs).Methods("POST")
	jsonBody, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", "/v1/utxos", bytes.NewBuffer(jsonBody))
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

// TestHandleGetUTXOs_IncludeMempoolDefaultOn verifies that mempool UTXOs are
// merged into the response when the client omits include_mempool.
func TestHandleGetUTXOs_IncludeMempoolDefaultOn(t *testing.T) {
	addr := "bc1qtestaddr"
	confirmed := neutrino.UTXO{
		TxID:    "aaaa",
		Vout:    0,
		Value:   1000,
		Address: addr,
		Height:  800000,
	}
	mempool := neutrino.MempoolUTXO{
		TxID:      "bbbb",
		Vout:      1,
		Value:     2000,
		Address:   addr,
		FirstSeen: 12345,
	}

	mock := &mockNode{
		getUTXOs: func([]string) ([]neutrino.UTXO, error) {
			return []neutrino.UTXO{confirmed}, nil
		},
		mempoolUTXOs: func([]string) []neutrino.MempoolUTXO {
			return []neutrino.MempoolUTXO{mempool}
		},
	}
	rr := postUTXOs(t, NewHandler(mock, newTestLogger()), map[string]any{
		"addresses": []string{addr},
	})

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		UTXOs []neutrino.UTXO `json:"utxos"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.UTXOs) != 2 {
		t.Fatalf("expected 2 utxos (confirmed + mempool), got %d", len(resp.UTXOs))
	}
	// The mempool entry must be the one with Height==0.
	var got neutrino.UTXO
	for _, u := range resp.UTXOs {
		if u.Height == 0 {
			got = u
			break
		}
	}
	if got.TxID != "bbbb" || got.Vout != 1 || got.Value != 2000 {
		t.Errorf("unexpected mempool entry: %+v", got)
	}
}

// TestHandleGetUTXOs_IncludeMempoolOptOut verifies that include_mempool=false
// suppresses the mempool merge.
func TestHandleGetUTXOs_IncludeMempoolOptOut(t *testing.T) {
	addr := "bc1qtestaddr"
	called := false
	mock := &mockNode{
		getUTXOs: func([]string) ([]neutrino.UTXO, error) {
			return []neutrino.UTXO{}, nil
		},
		mempoolUTXOs: func([]string) []neutrino.MempoolUTXO {
			called = true
			return []neutrino.MempoolUTXO{{TxID: "bbbb"}}
		},
	}
	includeMempool := false
	rr := postUTXOs(t, NewHandler(mock, newTestLogger()), map[string]any{
		"addresses":       []string{addr},
		"include_mempool": &includeMempool,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	if called {
		t.Error("GetMempoolUTXOs must not be invoked when include_mempool=false")
	}
	var resp struct {
		UTXOs []neutrino.UTXO `json:"utxos"`
	}
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.UTXOs) != 0 {
		t.Errorf("expected 0 utxos, got %d", len(resp.UTXOs))
	}
}

// TestHandleGetUTXOs_DedupConfirmedWinsOverMempool verifies that an outpoint
// present in both confirmed and mempool sets is reported once with the
// confirmed Height (mempool-tracker eviction lag should not double-count).
func TestHandleGetUTXOs_DedupConfirmedWinsOverMempool(t *testing.T) {
	addr := "bc1qtestaddr"
	mock := &mockNode{
		getUTXOs: func([]string) ([]neutrino.UTXO, error) {
			return []neutrino.UTXO{{TxID: "aaaa", Vout: 0, Value: 1000, Address: addr, Height: 800000}}, nil
		},
		mempoolUTXOs: func([]string) []neutrino.MempoolUTXO {
			return []neutrino.MempoolUTXO{{TxID: "aaaa", Vout: 0, Value: 1000, Address: addr}}
		},
	}
	rr := postUTXOs(t, NewHandler(mock, newTestLogger()), map[string]any{
		"addresses": []string{addr},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	var resp struct {
		UTXOs []neutrino.UTXO `json:"utxos"`
	}
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.UTXOs) != 1 {
		t.Fatalf("expected dedup to 1 entry, got %d", len(resp.UTXOs))
	}
	if resp.UTXOs[0].Height != 800000 {
		t.Errorf("expected confirmed Height to win, got %d", resp.UTXOs[0].Height)
	}
}

// TestHandleGetUTXO_MempoolSpendOverlay verifies that an unconfirmed spend is
// surfaced via mempool_* fields when the on-chain UTXO is still unspent.
func TestHandleGetUTXO_MempoolSpendOverlay(t *testing.T) {
	mock := &mockNode{
		getUTXO: func(string, uint32, string, int32) (*neutrino.UTXOSpendReport, error) {
			return &neutrino.UTXOSpendReport{Unspent: true, Value: 5000, ScriptPubKey: "00"}, nil
		},
		mempoolSpend: func(txid string, vout uint32) (neutrino.MempoolSpend, bool) {
			return neutrino.MempoolSpend{
				SpendingTxID: "cccc",
				InputIndex:   2,
				FirstSeen:    99,
			}, true
		},
	}
	router := mux.NewRouter()
	router.HandleFunc("/v1/utxo/{txid}/{vout}", NewHandler(mock, newTestLogger()).handleGetUTXO).Methods("GET")
	req, _ := http.NewRequest("GET", "/v1/utxo/aaaa/0?address=bc1q&start_height=1", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	var resp map[string]any
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if got := resp["mempool_spending_txid"]; got != "cccc" {
		t.Errorf("expected mempool_spending_txid=cccc, got %v", got)
	}
	if got := resp["unspent"].(bool); !got {
		t.Error("expected unspent=true (mempool overlay only)")
	}
}

// TestHandleGetUTXO_MempoolOptOut verifies that include_mempool=false skips
// the mempool overlay even when a tracker spend exists.
func TestHandleGetUTXO_MempoolOptOut(t *testing.T) {
	called := false
	mock := &mockNode{
		getUTXO: func(string, uint32, string, int32) (*neutrino.UTXOSpendReport, error) {
			return &neutrino.UTXOSpendReport{Unspent: true, Value: 5000}, nil
		},
		mempoolSpend: func(string, uint32) (neutrino.MempoolSpend, bool) {
			called = true
			return neutrino.MempoolSpend{SpendingTxID: "cccc"}, true
		},
	}
	router := mux.NewRouter()
	router.HandleFunc("/v1/utxo/{txid}/{vout}", NewHandler(mock, newTestLogger()).handleGetUTXO).Methods("GET")
	req, _ := http.NewRequest("GET", "/v1/utxo/aaaa/0?address=bc1q&include_mempool=false", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	if called {
		t.Error("GetMempoolSpend must not be called when include_mempool=false")
	}
	var resp map[string]any
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if _, ok := resp["mempool_spending_txid"]; ok {
		t.Error("mempool_spending_txid must not be present when opted out")
	}
}

// TestHandleGetTransaction_MempoolHit verifies that the tx endpoint returns
// the serialized tx hex when the mempool tracker holds a watched entry.
func TestHandleGetTransaction_MempoolHit(t *testing.T) {
	tx := wire.NewMsgTx(2)
	mock := &mockNode{
		mempoolTx: func(txid string) (*wire.MsgTx, bool) {
			return tx, true
		},
	}
	router := mux.NewRouter()
	router.HandleFunc("/v1/tx/{txid}", NewHandler(mock, newTestLogger()).handleGetTransaction).Methods("GET")
	req, _ := http.NewRequest("GET", "/v1/tx/abcd", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["mempool"] != true {
		t.Errorf("expected mempool=true, got %v", resp["mempool"])
	}
	if _, ok := resp["hex"].(string); !ok {
		t.Error("expected hex string in response")
	}
}

// TestHandleGetTransaction_MempoolMissFallsThrough verifies the existing
// not-implemented response when the tracker has no entry.
func TestHandleGetTransaction_MempoolMissFallsThrough(t *testing.T) {
	mock := &mockNode{}
	router := mux.NewRouter()
	router.HandleFunc("/v1/tx/{txid}", NewHandler(mock, newTestLogger()).handleGetTransaction).Methods("GET")
	req, _ := http.NewRequest("GET", "/v1/tx/abcd", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", rr.Code)
	}
}

// TestHandleGetStatus_IncludesMempoolFields verifies that /v1/status surfaces
// MempoolEnabled and the embedded MempoolStats when the tracker is active.
func TestHandleGetStatus_IncludesMempoolFields(t *testing.T) {
	mempool := neutrino.MempoolStats{Entries: 7, UTXOs: 5, Spends: 2, Peers: 4}
	mock := &mockNode{
		getStatus: func() neutrino.Status {
			return neutrino.Status{
				Synced:           true,
				BlockHeight:      900000,
				FilterHeight:     900000,
				Peers:            4,
				Version:          testVersion,
				WatchedAddresses: 1,
				MempoolEnabled:   true,
				Mempool:          &mempool,
			}
		},
	}
	router := mux.NewRouter()
	router.HandleFunc("/v1/status", NewHandler(mock, newTestLogger(), testVersion).handleGetStatus).Methods("GET")
	req, _ := http.NewRequest("GET", "/v1/status", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp neutrino.Status
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.MempoolEnabled {
		t.Error("expected mempool_enabled=true")
	}
	if resp.Mempool == nil {
		t.Fatal("expected non-nil mempool stats")
	}
	if *resp.Mempool != mempool {
		t.Errorf("mempool stats mismatch: got %+v want %+v", *resp.Mempool, mempool)
	}
}

// TestHandleGetStatus_OmitsMempoolWhenDisabled verifies that the mempool
// field is omitted from the JSON response when the tracker is disabled.
func TestHandleGetStatus_OmitsMempoolWhenDisabled(t *testing.T) {
	mock := &mockNode{
		getStatus: func() neutrino.Status {
			return neutrino.Status{
				Synced:         true,
				Version:        testVersion,
				MempoolEnabled: false,
				Mempool:        nil,
			}
		},
	}
	router := mux.NewRouter()
	router.HandleFunc("/v1/status", NewHandler(mock, newTestLogger(), testVersion).handleGetStatus).Methods("GET")
	req, _ := http.NewRequest("GET", "/v1/status", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	var raw map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, present := raw["mempool"]; present {
		t.Error("mempool field must be omitted when nil")
	}
	if got, _ := raw["mempool_enabled"].(bool); got {
		t.Error("expected mempool_enabled=false")
	}
}

// TestHandleGetStatus_IncludesTxHistoryFlag verifies /v1/status surfaces the
// tx_history_enabled capability flag.
func TestHandleGetStatus_IncludesTxHistoryFlag(t *testing.T) {
	mock := &mockNode{
		getStatus: func() neutrino.Status {
			return neutrino.Status{Synced: true, Version: testVersion, TxHistoryEnabled: true}
		},
	}
	router := mux.NewRouter()
	router.HandleFunc("/v1/status", NewHandler(mock, newTestLogger(), testVersion).handleGetStatus).Methods("GET")
	req, _ := http.NewRequest("GET", "/v1/status", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	var resp neutrino.Status
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.TxHistoryEnabled {
		t.Error("expected tx_history_enabled=true")
	}
}

// TestHandleGetTransactions verifies the /v1/transactions endpoint returns the
// node's history response and parses since_height.
func TestHandleGetTransactions(t *testing.T) {
	var gotSince int32
	mock := &mockNode{
		txHistory: func(sinceHeight int32) (neutrino.TxHistoryResponse, error) {
			gotSince = sinceHeight
			return neutrino.TxHistoryResponse{
				Transactions: []neutrino.TxHistoryRecord{
					{TxID: "aa", Hex: "00", Height: 205, Confirmed: true, Direction: "receive"},
					{TxID: "bb", Hex: "01", Height: 0, Confirmed: false, Direction: "spend"},
				},
				Cursor: 205,
			}, nil
		},
	}
	router := mux.NewRouter()
	router.HandleFunc("/v1/transactions", NewHandler(mock, newTestLogger(), testVersion).handleGetTransactions).Methods("GET")
	req, _ := http.NewRequest("GET", "/v1/transactions?since_height=200", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotSince != 200 {
		t.Errorf("expected since_height=200 forwarded, got %d", gotSince)
	}
	var resp neutrino.TxHistoryResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Transactions) != 2 || resp.Cursor != 205 {
		t.Errorf("unexpected response: %+v", resp)
	}
}

// TestHandleGetTransactions_InvalidSince rejects a malformed since_height.
func TestHandleGetTransactions_InvalidSince(t *testing.T) {
	router := mux.NewRouter()
	router.HandleFunc("/v1/transactions", NewHandler(&mockNode{}, newTestLogger(), testVersion).handleGetTransactions).Methods("GET")
	req, _ := http.NewRequest("GET", "/v1/transactions?since_height=abc", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}
