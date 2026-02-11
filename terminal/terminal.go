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
	id    string
	seqNo int // unique session sequence number for logging
	cmd   *exec.Cmd
	ptmx  *os.File

	mu   sync.Mutex
	conn *websocket.Conn // current active WebSocket, guarded by mu
	connSeq int          // incremented on each WebSocket swap
}

type Manager struct {
	mu          sync.RWMutex
	sessions    map[string]*Session
	resolveNode NodeResolver
	nextSeq     int // global session sequence counter
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
		log.Printf("[SESSION] isAlive S%d (%q): Process is nil → false", s.seqNo, s.id)
		return false
	}
	err := s.cmd.Process.Signal(syscall.Signal(0))
	if err != nil {
		log.Printf("[SESSION] isAlive S%d (%q): Signal(0) to pid %d failed: %v → false",
			s.seqNo, s.id, s.cmd.Process.Pid, err)
	}
	return err == nil
}

func (m *Manager) GetOrCreate(id string) (*Session, error) {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()

	if ok {
		alive := isAlive(s)
		log.Printf("[SESSION] GetOrCreate(%q): found existing session S%d, isAlive=%v (pid=%d)",
			id, s.seqNo, alive, s.cmd.Process.Pid)
		if alive {
			return s, nil
		}
		log.Printf("[SESSION] GetOrCreate(%q): existing session S%d is DEAD, will create new", id, s.seqNo)
	} else {
		log.Printf("[SESSION] GetOrCreate(%q): no session in map, will create new", id)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	s, ok = m.sessions[id]
	if ok && isAlive(s) {
		log.Printf("[SESSION] GetOrCreate(%q): double-check found alive session S%d, reusing", id, s.seqNo)
		return s, nil
	}

	m.nextSeq++
	seqNo := m.nextSeq

	cmd := m.buildCommand(id)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("starting pty for %s: %w", id, err)
	}

	log.Printf("[SESSION] GetOrCreate(%q): CREATED new session S%d (pid=%d)", id, seqNo, cmd.Process.Pid)

	s = &Session{
		id:    id,
		seqNo: seqNo,
		cmd:   cmd,
		ptmx:  ptmx,
	}
	m.sessions[id] = s

	// Persistent PTY reader: reads from PTY and writes to whatever
	// WebSocket connection is currently active. This goroutine lives
	// for the lifetime of the session, preventing duplicate readers
	// when clients reconnect.
	go func() {
		log.Printf("[PTY-READER] S%d (%q): goroutine started", seqNo, id)
		buf := make([]byte, 4096)
		for {
			n, err := s.ptmx.Read(buf)
			if n > 0 {
				s.mu.Lock()
				if s.conn != nil {
					if werr := s.conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
						log.Printf("[PTY-READER] S%d (%q): write to WS C%d failed: %v, clearing conn",
							seqNo, id, s.connSeq, werr)
						s.conn = nil
					}
				}
				s.mu.Unlock()
			}
			if err != nil {
				log.Printf("[PTY-READER] S%d (%q): PTY read error (goroutine exiting): %v", seqNo, id, err)
				return
			}
		}
	}()

	// Cleanup: remove session from map when process exits.
	go func() {
		err := cmd.Wait()
		log.Printf("[SESSION] S%d (%q): process exited (err=%v, state=%v)", seqNo, id, err, cmd.ProcessState)
		ptmx.Close()
		m.mu.Lock()
		if m.sessions[id] == s {
			delete(m.sessions, id)
			log.Printf("[SESSION] S%d (%q): removed from session map", seqNo, id)
		} else {
			log.Printf("[SESSION] S%d (%q): already replaced in session map, not removing", seqNo, id)
		}
		m.mu.Unlock()
	}()

	return s, nil
}

func (m *Manager) ServeWebSocket(conn *websocket.Conn, id string) {
	s, err := m.GetOrCreate(id)
	if err != nil {
		log.Printf("[WS] terminal %s: %v", id, err)
		conn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
		conn.Close()
		return
	}

	// Swap in the new connection; close the old one so its client-side
	// onmessage handler stops firing (prevents duplicate output).
	s.mu.Lock()
	old := s.conn
	s.connSeq++
	cseq := s.connSeq
	s.conn = conn
	s.mu.Unlock()

	hadOld := old != nil
	if old != nil {
		log.Printf("[WS] S%d (%q): swapped conn C%d → C%d (closing old)", s.seqNo, id, cseq-1, cseq)
		old.Close()
	} else {
		log.Printf("[WS] S%d (%q): set conn C%d (no previous conn)", s.seqNo, id, cseq)
	}
	_ = hadOld

	// Read input from this WebSocket and forward to PTY.
	log.Printf("[WS] S%d (%q) C%d: entering read loop", s.seqNo, id, cseq)
	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[WS] S%d (%q) C%d: read loop exiting: %v", s.seqNo, id, cseq, err)
			break
		}
		switch msgType {
		case websocket.BinaryMessage:
			s.ptmx.Write(data)
		case websocket.TextMessage:
			var msg resizeMsg
			if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" {
				log.Printf("[WS] S%d (%q) C%d: resize %dx%d", s.seqNo, id, cseq, msg.Cols, msg.Rows)
				pty.Setsize(s.ptmx, &pty.Winsize{
					Cols: msg.Cols,
					Rows: msg.Rows,
				})
			}
		}
	}

	// If we're still the active connection, nil it out.
	s.mu.Lock()
	wasActive := s.conn == conn
	if wasActive {
		s.conn = nil
	}
	s.mu.Unlock()
	log.Printf("[WS] S%d (%q) C%d: cleanup, wasActiveConn=%v", s.seqNo, id, cseq, wasActive)
}
