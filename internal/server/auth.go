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

// AuthMiddleware 验证 JWT 并注入 User 到 Context
func (s *Server) AuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 1. 获取 Token
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
		tokenString := parts[1]

		// 2. 解析并验证 Token
		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return jwtSecret, nil
		})

		if err != nil || !token.Valid {
			http.Error(w, "Invalid or expired token", http.StatusUnauthorized)
			return
		}

		// 3. 提取用户信息
		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			http.Error(w, "Invalid token claims", http.StatusUnauthorized)
			return
		}

		user, ok := claims["sub"].(string)
		if !ok || user == "" {
			http.Error(w, "Token missing 'sub' claim", http.StatusUnauthorized)
			return
		}

		// 4. 注入 Context 并放行
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
