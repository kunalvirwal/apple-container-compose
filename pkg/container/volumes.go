package container

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// VolumeClient manages named volumes through the container CLI.
type VolumeClient struct {
	run func(ctx context.Context, args ...string) (string, error)
}

// NewVolumeClient creates a VolumeClient using the supplied command runner.
func NewVolumeClient(run func(ctx context.Context, args ...string) (string, error)) VolumeClient {
	return VolumeClient{run: run}
}

// VolumeCreateOptions configures named-volume creation.
type VolumeCreateOptions struct {
	// Name is the name of the volume to create.
	Name string
	// Labels associates metadata with the volume as KEY=VALUE pairs.
	Labels map[string]string
	// Options contains volume driver options as KEY=VALUE pairs.
	Options map[string]string
}

// VolumeDeleteOptions configures named-volume deletion.
type VolumeDeleteOptions struct {
	// Names identifies the volumes to delete. It must be empty when All is set.
	Names []string
	// All deletes every volume.
	All bool
}

// VolumeSummary is the metadata returned by volume list operations.
type VolumeSummary struct {
	Name   string
	Labels map[string]string
}

// Create creates a named volume and returns the CLI output.
func (v *VolumeClient) Create(ctx context.Context, opts VolumeCreateOptions) (string, error) {
	if strings.TrimSpace(opts.Name) == "" {
		return "", ErrInvalidOptions
	}

	args := []string{"volume", "create"}
	for _, key := range sortedKeys(opts.Labels) {
		if strings.TrimSpace(key) == "" {
			return "", ErrInvalidOptions
		}
		args = append(args, "--label", key+"="+opts.Labels[key])
	}
	for _, key := range sortedKeys(opts.Options) {
		if strings.TrimSpace(key) == "" {
			return "", ErrInvalidOptions
		}
		args = append(args, "--opt", key+"="+opts.Options[key])
	}
	args = append(args, opts.Name)
	return v.runCommand(ctx, args...)
}

// Inspect returns the JSON description of the named volumes.
func (v *VolumeClient) Inspect(ctx context.Context, names []string) (string, error) {
	if len(names) == 0 {
		return "", ErrInvalidOptions
	}
	return v.runCommand(ctx, append([]string{"volume", "inspect"}, names...)...)
}

// Exists reports whether a named volume exists.
func (v *VolumeClient) Exists(ctx context.Context, name string) (bool, error) {
	_, exists, err := v.InspectSummary(ctx, name)
	return exists, err
}

// List returns the JSON description of all named volumes.
func (v *VolumeClient) List(ctx context.Context) (string, error) {
	return v.runCommand(ctx, "volume", "list", "--format", "json")
}

// ListSummaries returns names and labels for every named volume.
func (v *VolumeClient) ListSummaries(ctx context.Context) ([]VolumeSummary, error) {
	out, err := v.List(ctx)
	if err != nil {
		return nil, err
	}
	return decodeVolumeSummaries(out)
}

// InspectSummary returns metadata for a named volume and whether it exists.
func (v *VolumeClient) InspectSummary(ctx context.Context, name string) (VolumeSummary, bool, error) {
	if strings.TrimSpace(name) == "" {
		return VolumeSummary{}, false, ErrInvalidOptions
	}
	out, err := v.Inspect(ctx, []string{name})
	if err != nil {
		if isVolumeNotFoundError(out, err) {
			return VolumeSummary{}, false, nil
		}
		return VolumeSummary{}, false, err
	}

	summaries, err := decodeVolumeSummaries(out)
	if err != nil {
		return VolumeSummary{}, false, err
	}
	if len(summaries) != 1 {
		return VolumeSummary{}, false, fmt.Errorf("decode volume inspect output: expected one volume, got %d", len(summaries))
	}
	return summaries[0], true, nil
}

func decodeVolumeSummaries(out string) ([]VolumeSummary, error) {

	var response []struct {
		ID            string `json:"id"`
		Configuration struct {
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
		} `json:"configuration"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		return nil, fmt.Errorf("decode volume output: %w", err)
	}

	summaries := make([]VolumeSummary, 0, len(response))
	for _, item := range response {
		name := item.Configuration.Name
		if name == "" {
			name = item.ID
		}
		if name == "" {
			continue
		}
		summaries = append(summaries, VolumeSummary{Name: name, Labels: item.Configuration.Labels})
	}
	return summaries, nil
}

// Delete deletes the requested named volumes.
func (v *VolumeClient) Delete(ctx context.Context, opts VolumeDeleteOptions) (string, error) {
	if (opts.All && len(opts.Names) > 0) || (!opts.All && len(opts.Names) == 0) {
		return "", ErrInvalidOptions
	}
	args := []string{"volume", "delete"}
	if opts.All {
		args = append(args, "--all")
	} else {
		args = append(args, opts.Names...)
	}
	return v.runCommand(ctx, args...)
}

// Prune removes all volumes without container references.
func (v *VolumeClient) Prune(ctx context.Context) (string, error) {
	return v.runCommand(ctx, "volume", "prune")
}

func (v *VolumeClient) runCommand(ctx context.Context, args ...string) (string, error) {
	out, err := v.run(ctx, args...)
	if err != nil && strings.Contains(out, "XPC connection error") {
		return out, ErrSystemNotRunning
	}
	return out, err
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func isVolumeNotFoundError(output string, err error) bool {
	message := strings.ToLower(output)
	if err != nil {
		message += "\n" + strings.ToLower(err.Error())
	}
	return strings.Contains(message, "volume not found") || strings.Contains(message, "not found") || strings.Contains(message, "no such volume")
}
