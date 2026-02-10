package containers

import (
	"encoding/json"
	"fmt"
	"os/exec"
)

type Container struct {
	CTID   string `json:"ctid"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Type   string `json:"type,omitempty"`
	VMID   string `json:"vmid,omitempty"` // numeric ID for display
	Node   string `json:"node,omitempty"` // node the resource lives on
}

// NodeAddresses queries /cluster/status and returns a map of node name to
// IP address. This is used to resolve Proxmox node names (like "pve2") to
// routable IPs for SSH connections.
func NodeAddresses() (map[string]string, error) {
	out, err := exec.Command("pvesh", "get", "/cluster/status", "--output-format", "json").Output()
	if err != nil {
		return nil, fmt.Errorf("pvesh get /cluster/status: %w", err)
	}
	var entries []struct {
		Type   string `json:"type"`
		Name   string `json:"name"`
		IP     string `json:"ip"`
		Online int    `json:"online"`
	}
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("parsing cluster status: %w", err)
	}
	addrs := make(map[string]string)
	for _, e := range entries {
		if e.Type == "node" && e.IP != "" {
			addrs[e.Name] = e.IP
		}
	}
	return addrs, nil
}

// ListAll returns all cluster resources (nodes, LXC containers, VMs) by
// querying pvesh /cluster/resources and /cluster/status. Container CTIDs
// use the format "lxc/{node}/{vmid}" or "qemu/{node}/{vmid}" so the
// terminal manager can route connections to the correct node.
func ListAll() ([]Container, error) {
	out, err := exec.Command("pvesh", "get", "/cluster/resources", "--output-format", "json").Output()
	if err != nil {
		return nil, fmt.Errorf("pvesh get /cluster/resources: %w", err)
	}

	var raw []struct {
		Node   string `json:"node"`
		VMID   int    `json:"vmid"`
		Name   string `json:"name"`
		Status string `json:"status"`
		Type   string `json:"type"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parsing cluster resources: %w", err)
	}

	seenNodes := make(map[string]bool)
	var result []Container

	for _, r := range raw {
		switch r.Type {
		case "node":
			key := "node:" + r.Node
			if !seenNodes[key] {
				seenNodes[key] = true
				result = append(result, Container{
					CTID:   key,
					Name:   r.Node,
					Status: r.Status,
					Type:   "node",
				})
			}
		case "lxc", "qemu":
			vmidStr := fmt.Sprintf("%d", r.VMID)
			name := r.Name
			if name == "" {
				name = vmidStr
			}
			result = append(result, Container{
				CTID:   r.Type + "/" + r.Node + "/" + vmidStr,
				Name:   name,
				Status: r.Status,
				Type:   r.Type,
				VMID:   vmidStr,
				Node:   r.Node,
			})
		}
	}

	// If /cluster/resources didn't include node entries (happens on some
	// Proxmox configurations), supplement from /cluster/status.
	if len(seenNodes) == 0 {
		nodeAddrs, err := NodeAddresses()
		if err == nil {
			for name := range nodeAddrs {
				result = append(result, Container{
					CTID:   "node:" + name,
					Name:   name,
					Status: "online",
					Type:   "node",
				})
			}
		}
	}

	return result, nil
}
