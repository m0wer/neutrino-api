package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/btcsuite/btclog"
)

const (
	// TokenFilename is the default filename for the API auth token.
	TokenFilename = "auth_token"

	// tokenBytes is the number of random bytes in a generated token (32 bytes = 64 hex chars).
	tokenBytes = 32
)

// LoadOrGenerateToken returns the API authentication token for the server.
// If a token file exists it is loaded; otherwise a fresh random token is
// generated and written to disk with restricted permissions (0600).
func LoadOrGenerateToken(dataDir string, logger btclog.Logger) (string, error) {
	tokenPath := filepath.Join(dataDir, TokenFilename)

	// Try loading existing token.
	if fileExists(tokenPath) {
		logger.Infof("Loading auth token from %s", tokenPath)
		token, err := loadToken(tokenPath)
		if err != nil {
			logger.Warnf("Existing token file unreadable, regenerating: %v", err)
		} else if len(token) > 0 {
			return token, nil
		}
	}

	logger.Infof("Generating new auth token in %s", tokenPath)
	token, err := generateToken()
	if err != nil {
		return "", fmt.Errorf("failed to generate auth token: %w", err)
	}

	if err := writeToken(tokenPath, token); err != nil {
		return "", fmt.Errorf("failed to write auth token: %w", err)
	}

	return token, nil
}

// ValidateToken performs a constant-time comparison of two tokens.
func ValidateToken(provided, expected string) bool {
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func generateToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func loadToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func writeToken(path, token string) error {
	return os.WriteFile(path, []byte(token+"\n"), 0600)
}
