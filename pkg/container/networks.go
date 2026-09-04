package container

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// NetworkClient manages user-defined networks through the container CLI.
type NetworkClient struct {
	run func(ctx context.Context, args ...string) (string, error)
}

// NewNetworkClient creates a NetworkClient using the supplied command runner.
func NewNetworkClient(run func(ctx context.Context, args ...string) (string, error)) NetworkClient {
	return NetworkClient{run: run}
}

// NetworkCreateOptions configures user-defined network creation.
type NetworkCreateOptions struct {
	// Name is the name of the network to create.
	Name string
	// Labels associates metadata with the network as KEY=VALUE pairs.
	Labels map[string]string
}

// NetworkSummary is the network metadata used by higher-level clients to
// identify an ACC-managed network.
type NetworkSummary struct {
	Name   string
	Labels map[string]string
}

// Create creates a user-defined network and returns the CLI output.
func (n *NetworkClient) Create(ctx context.Context, opts NetworkCreateOptions) (string, error) {
	if strings.TrimSpace(opts.Name) == "" {
		return "", ErrInvalidOptions
	}

	args := []string{"network", "create"}
	for _, key := range sortedKeys(opts.Labels) {
		if strings.TrimSpace(key) == "" {
			return "", ErrInvalidOptions
		}
		args = append(args, "--label", key+"="+opts.Labels[key])
	}
	args = append(args, opts.Name)
	return n.runCommand(ctx, args...)
}

// Inspect returns the JSON description of the requested networks.
func (n *NetworkClient) Inspect(ctx context.Context, names []string) (string, error) {
	if len(names) == 0 {
		return "", ErrInvalidOptions
	}
	return n.runCommand(ctx, append([]string{"network", "inspect"}, names...)...)
}

// List returns the JSON description of all networks.
func (n *NetworkClient) List(ctx context.Context) (string, error) {
	return n.runCommand(ctx, "network", "list", "--format", "json")
}

// ListSummaries returns names and labels for every network.
func (n *NetworkClient) ListSummaries(ctx context.Context) ([]NetworkSummary, error) {
	out, err := n.List(ctx)
	if err != nil {
		return nil, err
	}
	return decodeNetworkSummaries(out)
}

// Exists reports whether a user-defined network exists.
func (n *NetworkClient) Exists(ctx context.Context, name string) (bool, error) {
	_, exists, err := n.InspectSummary(ctx, name)
	return exists, err
}

// InspectSummary returns metadata for a network and whether it exists.
func (n *NetworkClient) InspectSummary(ctx context.Context, name string) (NetworkSummary, bool, error) {
	if strings.TrimSpace(name) == "" {
		return NetworkSummary{}, false, ErrInvalidOptions
	}
	out, err := n.Inspect(ctx, []string{name})
	if err != nil {
		if isNetworkNotFoundError(out, err) {
			return NetworkSummary{}, false, nil
		}
		return NetworkSummary{}, false, err
	}

	summaries, err := decodeNetworkSummaries(out)
	if err != nil {
		return NetworkSummary{}, false, err
	}
	if len(summaries) != 1 {
		return NetworkSummary{}, false, fmt.Errorf("decode network inspect output: expected one network, got %d", len(summaries))
	}
	return summaries[0], true, nil
}

func decodeNetworkSummaries(out string) ([]NetworkSummary, error) {
	var response []struct {
		ID            string `json:"id"`
		Configuration struct {
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
		} `json:"configuration"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		return nil, fmt.Errorf("decode network output: %w", err)
	}

	summaries := make([]NetworkSummary, 0, len(response))
	for _, item := range response {
		name := item.Configuration.Name
		if name == "" {
			name = item.ID
		}
		if name == "" {
			continue
		}
		summaries = append(summaries, NetworkSummary{Name: name, Labels: item.Configuration.Labels})
	}
	return summaries, nil
}

// Delete deletes the named networks.
func (n *NetworkClient) Delete(ctx context.Context, names []string) (string, error) {
	if len(names) == 0 {
		return "", ErrInvalidOptions
	}
	args := make([]string, 0, len(names)+2)
	args = append(args, "network", "delete")
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			return "", ErrInvalidOptions
		}
		args = append(args, name)
	}
	return n.runCommand(ctx, args...)
}

func (n *NetworkClient) runCommand(ctx context.Context, args ...string) (string, error) {
	out, err := n.run(ctx, args...)
	if err != nil && strings.Contains(out, "XPC connection error") {
		return out, ErrSystemNotRunning
	}
	return out, err
}

func isNetworkNotFoundError(output string, err error) bool {
	message := strings.ToLower(output)
	if err != nil {
		message += "\n" + strings.ToLower(err.Error())
	}
	return strings.Contains(message, "network not found") || strings.Contains(message, "not found") || strings.Contains(message, "no such network")
}
