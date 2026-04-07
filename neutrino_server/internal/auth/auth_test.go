package auth

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/btcsuite/btclog"
)

func testLogger() btclog.Logger {
	backend := btclog.NewBackend(os.Stdout)
	l := backend.Logger("TEST")
	l.SetLevel(btclog.LevelOff)
	return l
}

// --- TLS Tests ---

func TestGenerateTLS_CreatesFiles(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.cert")
	keyPath := filepath.Join(dir, "tls.key")

	if err := generateTLS(certPath, keyPath); err != nil {
		t.Fatalf("generateTLS failed: %v", err)
	}

	if !fileExists(certPath) {
		t.Error("cert file not created")
	}
	if !fileExists(keyPath) {
		t.Error("key file not created")
	}

	info, _ := os.Stat(keyPath)
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("key file permissions = %o, want 0600", perm)
	}
}

func TestGenerateTLS_ValidCertificate(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.cert")
	keyPath := filepath.Join(dir, "tls.key")

	if err := generateTLS(certPath, keyPath); err != nil {
		t.Fatalf("generateTLS failed: %v", err)
	}

	certPEM, _ := os.ReadFile(certPath)
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("failed to decode cert PEM")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("failed to parse certificate: %v", err)
	}

	if len(cert.IPAddresses) < 2 {
		t.Errorf("expected at least 2 IP SANs, got %d", len(cert.IPAddresses))
	}
	if len(cert.DNSNames) < 1 || cert.DNSNames[0] != "localhost" {
		t.Errorf("expected DNS SAN 'localhost', got %v", cert.DNSNames)
	}

	if cert.Issuer.CommonName != cert.Subject.CommonName {
		t.Error("certificate is not self-signed")
	}
}

func TestLoadOrGenerateTLS_GeneratesOnFirstStart(t *testing.T) {
	dir := t.TempDir()
	logger := testLogger()

	tlsCfg, err := LoadOrGenerateTLS(dir, logger)
	if err != nil {
		t.Fatalf("LoadOrGenerateTLS failed: %v", err)
	}

	if tlsCfg.Config == nil {
		t.Error("TLS config is nil")
	}
	if tlsCfg.CertPath == "" || tlsCfg.KeyPath == "" {
		t.Error("cert/key paths are empty")
	}
}

func TestLoadOrGenerateTLS_LoadsExisting(t *testing.T) {
	dir := t.TempDir()
	logger := testLogger()

	tlsCfg1, err := LoadOrGenerateTLS(dir, logger)
	if err != nil {
		t.Fatalf("first call failed: %v", err)
	}

	certBytes1, _ := os.ReadFile(tlsCfg1.CertPath)

	tlsCfg2, err := LoadOrGenerateTLS(dir, logger)
	if err != nil {
		t.Fatalf("second call failed: %v", err)
	}

	certBytes2, _ := os.ReadFile(tlsCfg2.CertPath)
	if string(certBytes1) != string(certBytes2) {
		t.Error("second call regenerated certificate instead of loading existing")
	}
}

func TestLoadTLS_UsableForHTTPS(t *testing.T) {
	dir := t.TempDir()
	logger := testLogger()

	tlsCfg, err := LoadOrGenerateTLS(dir, logger)
	if err != nil {
		t.Fatalf("LoadOrGenerateTLS failed: %v", err)
	}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	}))
	srv.TLS = tlsCfg.Config
	srv.StartTLS()
	defer srv.Close()

	certPEM, _ := os.ReadFile(tlsCfg.CertPath)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("failed to add cert to pool")
	}

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs: pool,
			},
		},
	}

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("HTTPS request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// --- Token Tests ---

func TestGenerateToken_Length(t *testing.T) {
	token, err := generateToken()
	if err != nil {
		t.Fatalf("generateToken failed: %v", err)
	}
	if len(token) != 64 {
		t.Errorf("token length = %d, want 64", len(token))
	}
}

func TestGenerateToken_Unique(t *testing.T) {
	t1, _ := generateToken()
	t2, _ := generateToken()
	if t1 == t2 {
		t.Error("two generated tokens are identical")
	}
}

func TestLoadOrGenerateToken_GeneratesOnFirstStart(t *testing.T) {
	dir := t.TempDir()
	logger := testLogger()

	token, err := LoadOrGenerateToken(dir, logger)
	if err != nil {
		t.Fatalf("LoadOrGenerateToken failed: %v", err)
	}
	if len(token) != 64 {
		t.Errorf("token length = %d, want 64", len(token))
	}

	tokenPath := filepath.Join(dir, TokenFilename)
	if !fileExists(tokenPath) {
		t.Error("token file not created")
	}

	info, _ := os.Stat(tokenPath)
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("token file permissions = %o, want 0600", perm)
	}
}

func TestLoadOrGenerateToken_LoadsExisting(t *testing.T) {
	dir := t.TempDir()
	logger := testLogger()

	token1, _ := LoadOrGenerateToken(dir, logger)
	token2, _ := LoadOrGenerateToken(dir, logger)

	if token1 != token2 {
		t.Error("second call returned different token")
	}
}

func TestValidateToken_CorrectToken(t *testing.T) {
	if !ValidateToken("abc123", "abc123") {
		t.Error("ValidateToken returned false for matching tokens")
	}
}

func TestValidateToken_WrongToken(t *testing.T) {
	if ValidateToken("abc123", "xyz789") {
		t.Error("ValidateToken returned true for mismatched tokens")
	}
}

func TestValidateToken_EmptyToken(t *testing.T) {
	if ValidateToken("", "abc123") {
		t.Error("ValidateToken returned true for empty provided token")
	}
}

// --- Middleware Tests ---

func TestMiddleware_ValidToken(t *testing.T) {
	token := "testtoken123"
	logger := testLogger()

	handler := Middleware(token, logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestMiddleware_MissingHeader(t *testing.T) {
	token := "testtoken123"
	logger := testLogger()

	handler := Middleware(token, logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/v1/status", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestMiddleware_WrongToken(t *testing.T) {
	logger := testLogger()

	handler := Middleware("correct", logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestMiddleware_MalformedHeader(t *testing.T) {
	logger := testLogger()

	handler := Middleware("token", logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/v1/status", nil)
	req.Header.Set("Authorization", "token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestMiddleware_CaseInsensitiveBearer(t *testing.T) {
	token := "mytoken"
	logger := testLogger()

	handler := Middleware(token, logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/v1/status", nil)
	req.Header.Set("Authorization", "bearer "+token)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}
