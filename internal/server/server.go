package server

import (
	"fmt"
	"log"
	"net/http"
	"time"
)

type Config struct {
	Port    int
	HomeDir string
}

type Server struct {
	config Config
}

func NewServer(config Config) *Server {
	return &Server{
		config: config,
	}
}

func (s *Server) Start() error {
	mux := http.NewServeMux()

	// 注册路由
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/api/v1/run", s.handleRunWorkflow)

	// 配置 Server
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", s.config.Port),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	log.Printf("🚀 Tofi Server listening on port %d", s.config.Port)
	return srv.ListenAndServe()
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}
