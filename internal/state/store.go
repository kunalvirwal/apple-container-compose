// Package state safely maintains the global registry consumed by ACC's
// CoreDNS plugin. It deliberately has no CoreDNS dependency: the JSON schema
// is the contract between the host-side ACC process and the plugin container.
package state

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"
)

const stateVersion = 1

// State is the versioned CoreDNS registry persisted by ACC.
type State struct {
	Version    int         `json:"version"`
	Containers []Container `json:"containers"`
}

// Container is one service endpoint in the global registry.
type Container struct {
	ID          string                       `json:"id"`
	Service     string                       `json:"service"`
	Nameservers []string                     `json:"nameservers,omitempty"`
	Networks    map[string]NetworkAttachment `json:"networks"`
}

// NetworkAttachment holds one container's addresses and Compose aliases on a
// runtime network.
type NetworkAttachment struct {
	Addresses []string `json:"addresses"`
	Aliases   []string `json:"aliases,omitempty"`
}

// Store atomically edits a single state file. Its lock file serializes
// read-modify-write cycles across independent ACC processes.
type Store struct {
	path     string
	lockPath string
}

// NewStore constructs a store for an absolute state-file path.
func NewStore(path string) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("state file path must be absolute")
	}
	return &Store{path: path, lockPath: path + ".lock"}, nil
}

// DefaultPath returns ACC's default host-global CoreDNS state path.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}
	return filepath.Join(home, ".acc", "coredns", "state.json"), nil
}

// Ensure creates a valid empty state file when one does not yet exist.
func (s *Store) Ensure(ctx context.Context) error {
	_, err := s.Update(ctx, func(*State) error { return nil })
	return err
}

// Read returns the current validated state. It does not create a missing file.
func (s *Store) Read(ctx context.Context) (State, error) {
	var state State
	err := s.withLock(ctx, func() error {
		loaded, _, err := readState(s.path)
		if err != nil {
			return err
		}
		state = loaded
		return nil
	})
	return state, err
}

// Update serializes a state mutation, validates its result, and atomically
// replaces the state file. A missing state file is initialized as an empty
// version-1 registry before mutate is called.
func (s *Store) Update(ctx context.Context, mutate func(*State) error) (State, error) {
	if mutate == nil {
		return State{}, fmt.Errorf("state mutation cannot be nil")
	}
	var updated State
	err := s.withLock(ctx, func() error {
		state, previousModTime, err := readState(s.path)
		if os.IsNotExist(err) {
			state = State{Version: stateVersion, Containers: []Container{}}
			previousModTime = time.Time{}
		} else if err != nil {
			return err
		}
		if err := mutate(&state); err != nil {
			return err
		}
		if err := validateAndCanonicalize(&state); err != nil {
			return err
		}
		if err := writeState(s.path, state, previousModTime); err != nil {
			return err
		}
		updated = state
		return nil
	})
	return updated, err
}

// Upsert replaces any existing record with the same container ID.
func (s *Store) Upsert(ctx context.Context, container Container) error {
	_, err := s.Update(ctx, func(state *State) error {
		containers := state.Containers[:0]
		for _, existing := range state.Containers {
			if existing.ID != container.ID {
				containers = append(containers, existing)
			}
		}
		state.Containers = append(containers, container)
		return nil
	})
	return err
}

// Remove deletes records with the supplied container IDs. Empty IDs are
// rejected to prevent an accidental broad mutation.
func (s *Store) Remove(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	remove := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if !validID(id) {
			return fmt.Errorf("invalid container id %q", id)
		}
		remove[id] = struct{}{}
	}
	_, err := s.Update(ctx, func(state *State) error {
		containers := state.Containers[:0]
		for _, container := range state.Containers {
			if _, found := remove[container.ID]; !found {
				containers = append(containers, container)
			}
		}
		state.Containers = containers
		return nil
	})
	return err
}

func (s *Store) withLock(ctx context.Context, operation func() error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	lock, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open state lock: %w", err)
	}
	defer lock.Close()
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return fmt.Errorf("lock state: %w", err)
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return operation()
}

func readState(path string) (State, time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return State{}, time.Time{}, err
	}
	if !info.Mode().IsRegular() {
		return State{}, time.Time{}, fmt.Errorf("state file %q must be a regular file", path)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return State{}, time.Time{}, fmt.Errorf("read state file: %w", err)
	}
	var state State
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return State{}, time.Time{}, fmt.Errorf("decode state: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return State{}, time.Time{}, fmt.Errorf("state must contain exactly one JSON value")
	}
	if err := validateAndCanonicalize(&state); err != nil {
		return State{}, time.Time{}, err
	}
	return state, info.ModTime(), nil
}

func writeState(path string, state State, previousModTime time.Time) error {
	contents, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	contents = append(contents, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return fmt.Errorf("create temporary state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("set temporary state permissions: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary state: %w", err)
	}
	stamp := time.Now()
	if !previousModTime.IsZero() && !stamp.After(previousModTime) {
		stamp = previousModTime.Add(time.Nanosecond)
	}
	if err := os.Chtimes(temporaryPath, stamp, stamp); err != nil {
		temporary.Close()
		return fmt.Errorf("set temporary state modification time: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary state: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}
	return nil
}

func validateAndCanonicalize(state *State) error {
	if state.Version != stateVersion {
		return fmt.Errorf("unsupported state version %d", state.Version)
	}
	if state.Containers == nil {
		return fmt.Errorf("containers must be an array; use [] for an empty registry")
	}
	ids := make(map[string]struct{}, len(state.Containers))
	addresses := make(map[netip.Addr]string)
	for index := range state.Containers {
		container := &state.Containers[index]
		if !validID(container.ID) {
			return fmt.Errorf("container id must be nonempty and contain no whitespace/control characters")
		}
		if _, found := ids[container.ID]; found {
			return fmt.Errorf("duplicate container id %q", container.ID)
		}
		ids[container.ID] = struct{}{}
		if _, err := normalizeName(container.Service); err != nil {
			return fmt.Errorf("container %q service: %w", container.ID, err)
		}
		if len(container.Networks) == 0 {
			return fmt.Errorf("container %q has no networks", container.ID)
		}
		if err := validateNameservers(container.Nameservers); err != nil {
			return fmt.Errorf("container %q nameservers: %w", container.ID, err)
		}
		for network, attachment := range container.Networks {
			if !validID(network) {
				return fmt.Errorf("container %q has invalid network id %q", container.ID, network)
			}
			if len(attachment.Addresses) == 0 {
				return fmt.Errorf("container %q network %q has no addresses", container.ID, network)
			}
			for _, raw := range attachment.Addresses {
				address, err := parseEndpointAddress(raw)
				if err != nil {
					return fmt.Errorf("container %q network %q: %w", container.ID, network, err)
				}
				if owner, found := addresses[address]; found && owner != container.ID {
					return fmt.Errorf("address %s belongs to containers %q and %q", address, owner, container.ID)
				}
				addresses[address] = container.ID
			}
			for _, alias := range attachment.Aliases {
				if _, err := normalizeName(alias); err != nil {
					return fmt.Errorf("container %q network %q alias: %w", container.ID, network, err)
				}
			}
		}
	}
	sort.Slice(state.Containers, func(i, j int) bool { return state.Containers[i].ID < state.Containers[j].ID })
	return nil
}

func validateNameservers(nameservers []string) error {
	for _, raw := range nameservers {
		address, err := netip.ParseAddr(raw)
		if err != nil || address.Zone() != "" {
			return fmt.Errorf("invalid unscoped IP address %q", raw)
		}
		if !address.Unmap().IsGlobalUnicast() {
			return fmt.Errorf("nameserver %q must be a unicast address", raw)
		}
	}
	return nil
}

func parseEndpointAddress(raw string) (netip.Addr, error) {
	address, err := netip.ParseAddr(raw)
	if err != nil || address.Zone() != "" {
		return netip.Addr{}, fmt.Errorf("invalid unscoped IP address %q", raw)
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() {
		return netip.Addr{}, fmt.Errorf("address %q must be a unicast container address", raw)
	}
	return address, nil
}

func validID(value string) bool {
	return value != "" && !strings.ContainsFunc(value, func(character rune) bool {
		return unicode.IsSpace(character) || unicode.IsControl(character)
	})
}

func normalizeName(raw string) (string, error) {
	for _, character := range raw {
		if character > unicode.MaxASCII {
			return "", fmt.Errorf("DNS name must be ASCII: %q", raw)
		}
	}
	name := strings.TrimSuffix(strings.ToLower(raw), ".")
	if len(name) == 0 || len(name) > 253 {
		return "", fmt.Errorf("invalid DNS name length")
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("invalid DNS label in %q", raw)
		}
		for _, character := range label {
			if !((character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' || character == '_') {
				return "", fmt.Errorf("invalid DNS name %q", raw)
			}
		}
	}
	return name, nil
}
