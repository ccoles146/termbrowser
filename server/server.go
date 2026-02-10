package server

import (
	"encoding/json"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strconv"

	"github.com/chris/termbrowser/auth"
	"github.com/chris/termbrowser/config"
	"github.com/chris/termbrowser/containers"
	"github.com/chris/termbrowser/terminal"
	"github.com/gorilla/websocket"
)

type Server struct {
	cfg      *config.Config
	auth     *auth.Manager
	terminal *terminal.Manager
	webRoot  fs.FS
	upgrader websocket.Upgrader
}

func New(cfg *config.Config, a *auth.Manager, t *terminal.Manager, webRoot fs.FS) *Server {
	return &Server{
		cfg:      cfg,
		auth:     a,
		terminal: t,
		webRoot:  webRoot,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

func (s *Server) Run() error {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.Handle("GET /api/containers", s.auth.Middleware(http.HandlerFunc(s.handleContainers)))
	mux.Handle("GET /ws/terminal/{id}", s.auth.Middleware(http.HandlerFunc(s.handleTerminal)))
	mux.Handle("/", http.FileServer(http.FS(s.webRoot)))

	addr := net.JoinHostPort("", strconv.Itoa(s.cfg.Port))
	log.Printf("listening on %s", addr)
	return http.ListenAndServe(addr, mux)
}

type loginRequest struct {
	Password string `json:"password"`
	TOTPCode string `json:"totp_code"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.auth.Verify(req.Password, req.TOTPCode); err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	token, err := s.auth.IssueToken()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.auth.SetCookie(w, token)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.auth.ClearCookie(w)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleContainers(w http.ResponseWriter, r *http.Request) {
	list, err := containers.List()
	if err != nil {
		log.Printf("listing containers: %v", err)
		// Return empty list rather than error — host may not have pct
		list = nil
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id != "host" {
		if _, err := strconv.Atoi(id); err != nil {
			http.Error(w, "invalid terminal id", http.StatusBadRequest)
			return
		}
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade: %v", err)
		return
	}
	defer conn.Close()

	s.terminal.ServeWebSocket(conn, id)
}
