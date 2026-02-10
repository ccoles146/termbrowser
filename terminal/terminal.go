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

// NodeResolver maps a Proxmox node name to a routable address (IP or FQDN).
// It is called when building SSH commands for remote nodes/containers.
type NodeResolver func(name string) string

type Session struct {
	id   string
	cmd  *exec.Cmd
	ptmx *os.File

	mu   sync.Mutex
	conn *websocket.Conn // current active WebSocket, guarded by mu
}

type Manager struct {
	mu          sync.RWMutex
	sessions    map[string]*Session
	resolveNode NodeResolver
}

func NewManager(resolve NodeResolver) *Manager {
	return &Manager{
		sessions:    make(map[string]*Session),
		resolveNode: resolve,
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

// nodeAddr resolves a Proxmox node name to a routable address.
// Falls back to the raw name if no resolver is set or lookup fails.
func (m *Manager) nodeAddr(name string) string {
	if m.resolveNode != nil {
		if addr := m.resolveNode(name); addr != "" {
			return addr
		}
	}
	return name
}

func (m *Manager) buildCommand(id string) *exec.Cmd {
	var cmd *exec.Cmd
	switch {
	case id == "host":
		cmd = exec.Command("tmux", "new-session", "-A", "-s", "tb-host", "--", "/bin/bash")

	case strings.HasPrefix(id, "node:"):
		node := id[5:]
		addr := m.nodeAddr(node)
		session := "tb-" + strings.ReplaceAll(node, ".", "-")
		cmd = exec.Command("ssh", "-tt", "-o", "StrictHostKeyChecking=no", "root@"+addr,
			"env", "TERM=xterm-256color",
			"tmux", "new-session", "-A", "-s", session, "--", "/bin/bash")

	case strings.HasPrefix(id, "lxc/"):
		// Format: lxc/{node}/{vmid}
		parts := strings.SplitN(id[4:], "/", 2)
		node, vmid := parts[0], parts[1]
		addr := m.nodeAddr(node)
		cmd = exec.Command("ssh", "-tt", "-o", "StrictHostKeyChecking=no", "root@"+addr,
			"pct", "exec", vmid, "--",
			"env", "TERM=xterm-256color",
			"tmux", "new-session", "-A", "-s", "tb-"+vmid, "--", "/bin/bash")

	case strings.HasPrefix(id, "qemu/"):
		// Format: qemu/{node}/{vmid} — serial console via qm terminal
		parts := strings.SplitN(id[5:], "/", 2)
		node, vmid := parts[0], parts[1]
		addr := m.nodeAddr(node)
		cmd = exec.Command("ssh", "-tt", "-o", "StrictHostKeyChecking=no", "root@"+addr,
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

	cmd := m.buildCommand(id)
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

	// Persistent PTY reader: reads from PTY and writes to whatever
	// WebSocket connection is currently active. This goroutine lives
	// for the lifetime of the session, preventing duplicate readers
	// when clients reconnect.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := s.ptmx.Read(buf)
			if n > 0 {
				s.mu.Lock()
				if s.conn != nil {
					if werr := s.conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
						// Write failed; conn will be replaced on next connect.
						s.conn = nil
					}
				}
				s.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	// Cleanup: remove session from map when process exits.
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

	// Swap in the new connection; close the old one so its client-side
	// onmessage handler stops firing (prevents duplicate output).
	s.mu.Lock()
	old := s.conn
	s.conn = conn
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}

	// Read input from this WebSocket and forward to PTY.
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

	// If we're still the active connection, nil it out.
	s.mu.Lock()
	if s.conn == conn {
		s.conn = nil
	}
	s.mu.Unlock()
}
