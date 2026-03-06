package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var jwtSecret []byte

// InitAuth 初始化 JWT 密钥
// 如果环境变量 TOFI_JWT_SECRET 未设置，则生成一个随机密钥 (开发模式)
func InitAuth() {
	secret := os.Getenv("TOFI_JWT_SECRET")
	if secret == "" {
		jwtSecret = []byte(fmt.Sprintf("dev-secret-%d", time.Now().UnixNano()))
		log.Printf("⚠️  TOFI_JWT_SECRET not set. Generated temporary secret: %s", jwtSecret)
		log.Println("⚠️  Use this secret to sign tokens for testing.")
	} else {
		jwtSecret = []byte(secret)
	}
}

// GenerateToken 为指定用户生成一个长期有效的 Token (用于 CLI 测试)
func GenerateToken(username string, role string) (string, error) {
	if len(jwtSecret) == 0 {
		InitAuth()
	}
	claims := jwt.MapClaims{
		"sub":  username,
		"role": role,
		"iss":  "tofi-engine",
		"iat":  time.Now().Unix(),
		"exp":  time.Now().Add(365 * 24 * time.Hour).Unix(), // 1年有效期
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

type contextKey string

const UserContextKey contextKey = "user"

// parseJWT 解析并验证 JWT Token，返回用户 ID
func parseJWT(tokenString string) (string, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return jwtSecret, nil
	})
	if err != nil || !token.Valid {
		return "", fmt.Errorf("invalid or expired token")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", fmt.Errorf("invalid token claims")
	}

	user, ok := claims["sub"].(string)
	if !ok || user == "" {
		return "", fmt.Errorf("token missing 'sub' claim")
	}

	return user, nil
}

// AuthMiddleware 验证 JWT 并注入 User 到 Context
func (s *Server) AuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "Missing Authorization header", http.StatusUnauthorized)
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			http.Error(w, "Invalid Authorization format (expected 'Bearer <token>')", http.StatusUnauthorized)
			return
		}

		user, err := parseJWT(parts[1])
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), UserContextKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

// AdminMiddleware 验证用户是否为 Admin
// 在 AuthMiddleware 的基础上检查用户角色
func (s *Server) AdminMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return s.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		username := r.Context().Value(UserContextKey).(string)
		user, err := s.db.GetUser(username)
		if err != nil {
			http.Error(w, "User not found", http.StatusUnauthorized)
			return
		}
		if user.Role != "admin" {
			http.Error(w, "Admin access required", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
