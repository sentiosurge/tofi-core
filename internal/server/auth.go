package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var jwtSecret []byte

// InitAuth initializes JWT secret from TOFI_JWT_SECRET env var.
// If not set, generates a temporary secret (dev mode).
func InitAuth() {
	secret := os.Getenv("TOFI_JWT_SECRET")
	if secret == "" {
		jwtSecret = []byte(fmt.Sprintf("dev-secret-%d", time.Now().UnixNano()))
		log.Printf("TOFI_JWT_SECRET not set. Generated temporary secret for dev mode.")
	} else {
		jwtSecret = []byte(secret)
	}
}

// GenerateToken generates a long-lived JWT for the given user.
func GenerateToken(username string, role string) (string, error) {
	if len(jwtSecret) == 0 {
		InitAuth()
	}
	claims := jwt.MapClaims{
		"sub":  username,
		"role": role,
		"iss":  "tofi-engine",
		"iat":  time.Now().Unix(),
		"exp":  time.Now().Add(365 * 24 * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

type contextKey string

const UserContextKey contextKey = "user"
const RoleContextKey contextKey = "role"

// parseJWT parses and validates a JWT token, returning the username and role.
func parseJWT(tokenString string) (string, string, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return jwtSecret, nil
	})
	if err != nil || !token.Valid {
		return "", "", fmt.Errorf("invalid or expired token")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", "", fmt.Errorf("invalid token claims")
	}

	user, ok := claims["sub"].(string)
	if !ok || user == "" {
		return "", "", fmt.Errorf("token missing 'sub' claim")
	}

	role, _ := claims["role"].(string)

	return user, role, nil
}

// AuthMiddleware validates auth token and injects user into context.
// Supports two modes:
//   - Token mode: raw access_token from config (matched against s.accessToken)
//   - JWT mode: standard JWT signed with jwt_secret
func (s *Server) AuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			writeJSONError(w, http.StatusUnauthorized, ErrUnauthorized, "Missing Authorization header", "Include 'Authorization: Bearer <token>' in your request")
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			writeJSONError(w, http.StatusUnauthorized, ErrUnauthorized, "Invalid Authorization format", "Expected 'Authorization: Bearer <token>'")
			return
		}

		tokenStr := parts[1]

		// Check if it's a raw access token (token auth mode)
		if s.accessToken != "" && tokenStr == s.accessToken {
			ctx := context.WithValue(r.Context(), UserContextKey, "admin")
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// Check if it's an API key (tofi-sk-*)
		if strings.HasPrefix(tokenStr, "tofi-sk-") {
			h := sha256.Sum256([]byte(tokenStr))
			keyHash := hex.EncodeToString(h[:])
			rec, role, err := s.db.GetAPIKeyByHash(keyHash)
			if err != nil {
				writeJSONError(w, http.StatusInternalServerError, ErrInternal, err.Error(), "")
				return
			}
			if rec == nil {
				writeJSONError(w, http.StatusUnauthorized, ErrUnauthorized, "Invalid API key", "")
				return
			}
			// Check expiration
			if rec.ExpiresAt != nil {
				if exp, err := time.Parse(time.RFC3339, *rec.ExpiresAt); err == nil && time.Now().After(exp) {
					writeJSONError(w, http.StatusUnauthorized, ErrUnauthorized, "API key has expired", "Create a new key: POST /api/v1/user/api-keys")
					return
				}
			}
			go s.db.TouchAPIKeyLastUsed(rec.ID)
			ctx := context.WithValue(r.Context(), UserContextKey, rec.UserID)
			if role != nil {
				ctx = context.WithValue(ctx, RoleContextKey, *role)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// Otherwise try JWT
		user, role, err := parseJWT(tokenStr)
		if err != nil {
			writeJSONError(w, http.StatusUnauthorized, ErrUnauthorized, "Invalid or expired token", "Login again: POST /api/v1/auth/login")
			return
		}

		ctx := context.WithValue(r.Context(), UserContextKey, user)
		ctx = context.WithValue(ctx, RoleContextKey, role)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

// AdminMiddleware validates that the user is an admin.
func (s *Server) AdminMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return s.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		// Token-mode admin is always admin
		username := r.Context().Value(UserContextKey).(string)
		if username == "admin" && s.accessToken != "" {
			next.ServeHTTP(w, r)
			return
		}

		// Check JWT role claim first (covers TUI-generated JWTs)
		if role, ok := r.Context().Value(RoleContextKey).(string); ok && role == "admin" {
			next.ServeHTTP(w, r)
			return
		}

		// Fallback: check DB for user role
		user, err := s.db.GetUser(username)
		if err != nil {
			writeJSONError(w, http.StatusForbidden, ErrForbidden, "Admin access required", "")
			return
		}
		if user.Role != "admin" {
			writeJSONError(w, http.StatusForbidden, ErrForbidden, "Admin access required", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}
