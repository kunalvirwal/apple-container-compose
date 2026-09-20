// Package coredns manages ACC's per-project CoreDNS infrastructure container.
package coredns

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kunalvirwal/apple-container-compose/pkg/container"
)

const (
	projectLabel = "io.github.kunalvirwal.acc.project"
	roleLabel    = "io.github.kunalvirwal.acc.role"
	coreDNSRole  = "coredns"
	defaultImage = "docker.io/kunalvirwal/acc-coredns:v2"
)

// ManagerOptions configures the ACC CoreDNS infrastructure manager.
type ManagerOptions struct {
	// StatePath is the absolute host path of the state.json mounted into
	// CoreDNS. Its parent directory is mounted because ACC updates the file by
	// atomic replacement.
	StatePath string
	// Image is the CoreDNS image containing ACC's plugin. Empty uses ACC's
	// current public image reference.
	Image string
}

// Manager starts a dedicated CoreDNS container for one Compose project.
// It deliberately does not reuse an existing CoreDNS container: reconciliation
// of a changed Compose topology is a later, explicit lifecycle feature.
type Manager struct {
	runtime   *container.Client
	statePath string
	image     string
}

// NewManager constructs a CoreDNS manager using the given container runtime.
func NewManager(runtime *container.Client, opts ManagerOptions) (*Manager, error) {
	if runtime == nil {
		return nil, fmt.Errorf("container runtime cannot be nil")
	}
	if !filepath.IsAbs(opts.StatePath) {
		return nil, fmt.Errorf("CoreDNS state path must be absolute")
	}
	if filepath.Base(opts.StatePath) != "state.json" {
		return nil, fmt.Errorf("CoreDNS state file must be named state.json")
	}
	image := strings.TrimSpace(opts.Image)
	if image == "" {
		image = defaultImage
	}
	return &Manager{runtime: runtime, statePath: opts.StatePath, image: image}, nil
}

// Start creates a CoreDNS container named <project>-coredns on every supplied
// runtime network and returns its IPv4 address for each network. state.json
// must already exist and be valid; the host-side state store owns its creation.
func (m *Manager) Start(ctx context.Context, projectName string, networkNames []string) (map[string]netip.Addr, error) {
	if m == nil || m.runtime == nil {
		return nil, fmt.Errorf("CoreDNS manager is not initialized")
	}
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		return nil, fmt.Errorf("CoreDNS project name cannot be empty")
	}
	networks, err := validNetworkNames(networkNames)
	if err != nil {
		return nil, err
	}
	if len(networks) == 0 {
		return nil, fmt.Errorf("CoreDNS requires at least one network")
	}
	info, err := os.Stat(m.statePath)
	if err != nil {
		return nil, fmt.Errorf("inspect CoreDNS state file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("CoreDNS state path must be a regular file")
	}

	name := projectName + "-coredns"

	// Start the coreDNS container
	_, err = m.runtime.Container.Run(ctx, m.image, container.CreateOptions{
		Name: name,
		Labels: map[string]string{
			projectLabel: projectName,
			roleLabel:    coreDNSRole,
		},
		Networks: networks,
		Mounts: []container.Mount{{
			Type:     container.MountTypeBind,
			Source:   filepath.Dir(m.statePath),
			Target:   "/config",
			ReadOnly: true,
		}},
	})
	if err != nil {
		return nil, fmt.Errorf("start CoreDNS container %q: %w", name, err)
	}

	details, err := m.runtime.Container.InspectDetails(ctx, []string{name})
	if err != nil {
		return nil, fmt.Errorf("inspect CoreDNS container %q: %w", name, err)
	}
	if len(details) != 1 || details[0].ID == "" {
		return nil, fmt.Errorf("inspect CoreDNS container %q: expected one identified container", name)
	}
	return networkIPv4Addresses(details[0].Networks, networks)
}

// validNetworkNames trims, validates, deduplicates, and sorts network names.
func validNetworkNames(raw []string) ([]string, error) {
	set := make(map[string]struct{}, len(raw))
	for _, networkName := range raw {
		networkName = strings.TrimSpace(networkName)
		if networkName == "" {
			return nil, fmt.Errorf("CoreDNS network name cannot be empty")
		}
		set[networkName] = struct{}{}
	}
	networks := make([]string, 0, len(set))
	for networkName := range set {
		networks = append(networks, networkName)
	}
	sort.Strings(networks)
	return networks, nil
}

// networkIPv4Addresses returns one global-unicast IPv4 address for each requested network.
func networkIPv4Addresses(attachments []container.NetworkAttachment, networks []string) (map[string]netip.Addr, error) {
	wanted := make(map[string]struct{}, len(networks))
	for _, networkName := range networks {
		wanted[networkName] = struct{}{}
	}
	addresses := make(map[string]netip.Addr, len(networks))
	for _, attachment := range attachments {
		if _, ok := wanted[attachment.Name]; !ok {
			continue
		}
		for _, address := range attachment.Addresses {
			if address.Is4() && address.IsGlobalUnicast() {
				addresses[attachment.Name] = address
				break
			}
		}
	}
	for _, networkName := range networks {
		if _, found := addresses[networkName]; !found {
			return nil, fmt.Errorf("CoreDNS has no IPv4 address on network %q", networkName)
		}
	}
	return addresses, nil
}
