package storage

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"
)

// APIKeyRecord represents a stored API key.
type APIKeyRecord struct {
	ID         string  `json:"id"`
	UserID     string  `json:"user_id"`
	KeyPrefix  string  `json:"key_prefix"`
	Name       string  `json:"name"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at"`
	ExpiresAt  *string `json:"expires_at"`
}

func (db *DB) initAPIKeysTable() error {
	_, err := db.conn.Exec(`
	CREATE TABLE IF NOT EXISTS api_keys (
		id         TEXT PRIMARY KEY,
		user_id    TEXT NOT NULL,
		key_prefix TEXT NOT NULL,
		key_hash   TEXT NOT NULL UNIQUE,
		name       TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		last_used_at DATETIME,
		expires_at DATETIME
	);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_hash ON api_keys(key_hash);
	CREATE INDEX IF NOT EXISTS idx_api_keys_user ON api_keys(user_id);
	`)
	return err
}

// GenerateSecureToken generates a cryptographically random hex token and its SHA-256 hash.
func GenerateSecureToken(byteLen int) (token string, hash string) {
	b := make([]byte, byteLen)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	token = hex.EncodeToString(b)
	h := sha256.Sum256([]byte(token))
	hash = hex.EncodeToString(h[:])
	return token, hash
}

// CreateAPIKey stores a new API key record.
func (db *DB) CreateAPIKey(id, userID, prefix, keyHash, name string, expiresAt *time.Time) error {
	var exp *string
	if expiresAt != nil {
		s := expiresAt.UTC().Format(time.RFC3339)
		exp = &s
	}
	_, err := db.conn.Exec(
		`INSERT INTO api_keys (id, user_id, key_prefix, key_hash, name, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, userID, prefix, keyHash, name, exp,
	)
	return err
}

// ListAPIKeys returns all API keys for a user (without hashes).
func (db *DB) ListAPIKeys(userID string) ([]APIKeyRecord, error) {
	rows, err := db.conn.Query(
		`SELECT id, user_id, key_prefix, name, created_at, last_used_at, expires_at
		 FROM api_keys WHERE user_id = ? ORDER BY created_at DESC`, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []APIKeyRecord
	for rows.Next() {
		var k APIKeyRecord
		if err := rows.Scan(&k.ID, &k.UserID, &k.KeyPrefix, &k.Name, &k.CreatedAt, &k.LastUsedAt, &k.ExpiresAt); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, nil
}

// GetAPIKeyByHash looks up an API key by its SHA-256 hash. Returns nil if not found.
func (db *DB) GetAPIKeyByHash(keyHash string) (*APIKeyRecord, *string, error) {
	var k APIKeyRecord
	var userRole string
	var expiresAt sql.NullString

	err := db.conn.QueryRow(`
		SELECT ak.id, ak.user_id, ak.key_prefix, ak.name, ak.created_at, ak.last_used_at, ak.expires_at, u.role
		FROM api_keys ak
		JOIN users u ON u.username = ak.user_id
		WHERE ak.key_hash = ?`, keyHash,
	).Scan(&k.ID, &k.UserID, &k.KeyPrefix, &k.Name, &k.CreatedAt, &k.LastUsedAt, &expiresAt, &userRole)

	if err == sql.ErrNoRows {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if expiresAt.Valid {
		k.ExpiresAt = &expiresAt.String
	}
	return &k, &userRole, nil
}

// DeleteAPIKey removes an API key (only if owned by userID).
func (db *DB) DeleteAPIKey(id, userID string) error {
	result, err := db.conn.Exec(`DELETE FROM api_keys WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("api key not found")
	}
	return nil
}

// TouchAPIKeyLastUsed updates the last_used_at timestamp.
func (db *DB) TouchAPIKeyLastUsed(id string) {
	db.conn.Exec(`UPDATE api_keys SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
}
