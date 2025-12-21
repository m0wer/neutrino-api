package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btclog"
	"github.com/gorilla/mux"

	"github.com/yourusername/neutrino-api/neutrino_server/internal/neutrino"
)

// mockNode implements NodeInterface for testing
type mockNode struct{}

func (m *mockNode) GetStatus() neutrino.Status {
	return neutrino.Status{
		Synced:       true,
		BlockHeight:  8543,
		FilterHeight: 8543,
		Peers:        1,
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
	return []neutrino.UTXO{}, nil
}

func (m *mockNode) GetUTXO(txid string, vout uint32, address string, startHeight int32) (*neutrino.UTXOSpendReport, error) {
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

func (m *mockNode) Rescan(startHeight int32, addresses []string) error {
	return nil
}

func TestHandleGetStatus(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	handler := NewHandler(&mockNode{}, logger)

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

	handler := NewHandler(&mockNode{}, logger)

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
