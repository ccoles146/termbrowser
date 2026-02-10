package containers

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

type Container struct {
	CTID   string `json:"ctid"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Type   string `json:"type,omitempty"` // "lxc" or "node"
}

// List runs `pct list` and returns all LXC containers.
// Expected output format:
// VMID       Status     Lock         Name
// 100        running                 mycontainer
func List() ([]Container, error) {
	out, err := exec.Command("pct", "list").Output()
	if err != nil {
		return nil, fmt.Errorf("pct list: %w", err)
	}

	var containers []Container
	scanner := bufio.NewScanner(bytes.NewReader(out))
	first := true
	for scanner.Scan() {
		line := scanner.Text()
		if first {
			first = false
			continue // skip header
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		ctid := fields[0]
		status := fields[1]
		name := ""
		// Name is always the last field; if there's a Lock column it's 3rd,
		// making Name the 4th. If no Lock, Name is 3rd.
		if len(fields) >= 3 {
			name = fields[len(fields)-1]
		}
		containers = append(containers, Container{
			CTID:   ctid,
			Name:   name,
			Status: status,
			Type:   "lxc",
		})
	}
	return containers, scanner.Err()
}

// ListNodes runs `pvesh get /nodes` and returns all Proxmox cluster nodes.
func ListNodes() ([]Container, error) {
	out, err := exec.Command("pvesh", "get", "/nodes", "--output-format", "json").Output()
	if err != nil {
		return nil, fmt.Errorf("pvesh get /nodes: %w", err)
	}

	var raw []struct {
		Node   string `json:"node"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parsing nodes: %w", err)
	}

	nodes := make([]Container, 0, len(raw))
	for _, n := range raw {
		nodes = append(nodes, Container{
			CTID:   "node:" + n.Node,
			Name:   n.Node,
			Status: n.Status,
			Type:   "node",
		})
	}
	return nodes, nil
}
