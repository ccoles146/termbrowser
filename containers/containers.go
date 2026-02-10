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

// ListAll returns all cluster resources (nodes, LXC containers, VMs) by
// querying pvesh /cluster/resources. Container CTIDs use the format
// "lxc/{node}/{vmid}" or "qemu/{node}/{vmid}" so the terminal manager
// can route connections to the correct node.
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
	return result, nil
}
