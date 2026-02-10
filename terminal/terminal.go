package terminal

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

type resizeMsg struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

type Session struct {
	id   string
	cmd  *exec.Cmd
	ptmx *os.File
	mu   sync.Mutex
}

type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

func NewManager() *Manager {
	return &Manager{
		sessions: make(map[string]*Session),
	}
}

// buildEnv returns os.Environ() with any existing TERM removed, then
// TERM=xterm-256color appended. On Linux, getenv() returns the first
// match, so duplicate TERM entries would silently override our value.
func buildEnv() []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "TERM=") {
			env = append(env, e)
		}
	}
	return append(env, "TERM=xterm-256color")
}

func buildCommand(id string) *exec.Cmd {
	var cmd *exec.Cmd
	switch {
	case id == "host":
		cmd = exec.Command("tmux", "new-session", "-A", "-s", "tb-host", "--", "/bin/bash")

	case strings.HasPrefix(id, "node:"):
		node := id[5:]
		session := "tb-" + strings.ReplaceAll(node, ".", "-")
		cmd = exec.Command("ssh", "-tt", "-o", "StrictHostKeyChecking=no", "root@"+node,
			"env", "TERM=xterm-256color",
			"tmux", "new-session", "-A", "-s", session, "--", "/bin/bash")

	case strings.HasPrefix(id, "lxc/"):
		// Format: lxc/{node}/{vmid}
		parts := strings.SplitN(id[4:], "/", 2)
		node, vmid := parts[0], parts[1]
		cmd = exec.Command("ssh", "-tt", "-o", "StrictHostKeyChecking=no", "root@"+node,
			"pct", "exec", vmid, "--",
			"env", "TERM=xterm-256color",
			"tmux", "new-session", "-A", "-s", "tb-"+vmid, "--", "/bin/bash")

	case strings.HasPrefix(id, "qemu/"):
		// Format: qemu/{node}/{vmid} — serial console via qm terminal
		parts := strings.SplitN(id[5:], "/", 2)
		node, vmid := parts[0], parts[1]
		cmd = exec.Command("ssh", "-tt", "-o", "StrictHostKeyChecking=no", "root@"+node,
			"qm", "terminal", vmid, "-iface", "serial0")

	default:
		// Legacy: bare numeric ctid for local LXC container
		cmd = exec.Command("pct", "exec", id, "--",
			"env", "TERM=xterm-256color",
			"tmux", "new-session", "-A", "-s", "tb-"+id, "--", "/bin/bash")
	}

	cmd.Env = buildEnv()
	return cmd
}

func isAlive(s *Session) bool {
	if s.cmd.Process == nil {
		return false
	}
	// Signal 0 checks if process exists without delivering a real signal
	err := s.cmd.Process.Signal(syscall.Signal(0))
	return err == nil
}

func (m *Manager) GetOrCreate(id string) (*Session, error) {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()

	if ok && isAlive(s) {
		return s, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	s, ok = m.sessions[id]
	if ok && isAlive(s) {
		return s, nil
	}

	cmd := buildCommand(id)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("starting pty for %s: %w", id, err)
	}

	s = &Session{
		id:   id,
		cmd:  cmd,
		ptmx: ptmx,
	}
	m.sessions[id] = s

	go func() {
		cmd.Wait()
		ptmx.Close()
		m.mu.Lock()
		if m.sessions[id] == s {
			delete(m.sessions, id)
		}
		m.mu.Unlock()
	}()

	return s, nil
}

func (m *Manager) ServeWebSocket(conn *websocket.Conn, id string) {
	s, err := m.GetOrCreate(id)
	if err != nil {
		log.Printf("terminal %s: %v", id, err)
		conn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
		conn.Close()
		return
	}

	// PTY reader goroutine: send output to WebSocket
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := s.ptmx.Read(buf)
			if n > 0 {
				s.mu.Lock()
				werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n])
				s.mu.Unlock()
				if werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// WebSocket reader: forward input to PTY or handle resize
	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		switch msgType {
		case websocket.BinaryMessage:
			s.ptmx.Write(data)
		case websocket.TextMessage:
			var msg resizeMsg
			if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" {
				pty.Setsize(s.ptmx, &pty.Winsize{
					Cols: msg.Cols,
					Rows: msg.Rows,
				})
			}
		}
	}

	<-done
}
