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

// Manager reconciles a dedicated CoreDNS container for one Compose project.
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

// Start returns the IPv4 addresses of this project's CoreDNS container on each
// supplied runtime network. It reuses exactly one existing ACC-owned CoreDNS
// container only when it is already attached to every required network;
// otherwise it creates a new container. state.json must already exist and be
// valid; the host-side state store owns its creation.
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

	coreDNSIDs, _, err := m.projectContainerIDs(ctx, projectName)
	if err != nil {
		return nil, err
	}
	switch len(coreDNSIDs) {
	case 0:
		// Create the container below.
	case 1:
		return m.inspectReusableContainer(ctx, projectName, coreDNSIDs[0], networks)
	default:
		return nil, coreDNSReconciliationError(projectName, fmt.Sprintf("found %d ACC-managed CoreDNS containers", len(coreDNSIDs)))
	}

	name := projectName + "-coredns"

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
	return m.inspectContainer(ctx, name, networks)
}

func (m *Manager) inspectReusableContainer(ctx context.Context, projectName, id string, networks []string) (map[string]netip.Addr, error) {
	details, err := m.inspectContainerDetails(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("inspect existing CoreDNS container %q: %w", id, err)
	}
	if missing := missingNetworkNames(details.Networks, networks); len(missing) > 0 {
		return nil, coreDNSReconciliationError(projectName, fmt.Sprintf("existing CoreDNS container is not attached to required network %q", missing[0]))
	}
	addresses, err := networkIPv4Addresses(details.Networks, networks)
	if err != nil {
		return nil, coreDNSReconciliationError(projectName, fmt.Sprintf("existing CoreDNS container has unusable network addressing: %v", err))
	}
	return addresses, nil
}

func (m *Manager) inspectContainer(ctx context.Context, id string, networks []string) (map[string]netip.Addr, error) {
	details, err := m.inspectContainerDetails(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("inspect CoreDNS container %q: %w", id, err)
	}
	return networkIPv4Addresses(details.Networks, networks)
}

func (m *Manager) inspectContainerDetails(ctx context.Context, id string) (container.ContainerDetails, error) {
	details, err := m.runtime.Container.InspectDetails(ctx, []string{id})
	if err != nil {
		return container.ContainerDetails{}, err
	}
	if len(details) != 1 || details[0].ID == "" {
		return container.ContainerDetails{}, fmt.Errorf("expected one identified container")
	}
	return details[0], nil
}

func missingNetworkNames(attachments []container.NetworkAttachment, required []string) []string {
	attached := make(map[string]struct{}, len(attachments))
	for _, attachment := range attachments {
		attached[attachment.Name] = struct{}{}
	}
	missing := make([]string, 0)
	for _, networkName := range required {
		if _, found := attached[networkName]; !found {
			missing = append(missing, networkName)
		}
	}
	return missing
}

func coreDNSReconciliationError(projectName, reason string) error {
	return fmt.Errorf("cannot reconcile CoreDNS for project %q: %s; run `acc down --remove-orphans` using the earlier Compose file, then run `acc up` again", projectName, reason)
}

// RemoveIfUnused stops and deletes this project's CoreDNS containers only
// when no other project containers remain. This preserves DNS and all
// networks attached to it while Compose orphans are intentionally retained.
func (m *Manager) RemoveIfUnused(ctx context.Context, projectName string, force bool) error {
	if m == nil || m.runtime == nil {
		return fmt.Errorf("CoreDNS manager is not initialized")
	}
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		return fmt.Errorf("CoreDNS project name cannot be empty")
	}

	coreDNSIDs, otherProjectContainers, err := m.projectContainerIDs(ctx, projectName)
	if err != nil {
		return err
	}
	if otherProjectContainers {
		return nil
	}
	return m.removeContainers(ctx, coreDNSIDs, force)
}

func (m *Manager) projectContainerIDs(ctx context.Context, projectName string) ([]string, bool, error) {
	containers, err := m.runtime.Container.ListSummaries(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, false, fmt.Errorf("list CoreDNS containers: %w", err)
	}

	coreDNSIDs := make([]string, 0)
	otherProjectContainers := false
	for _, item := range containers {
		if item.Labels[projectLabel] != projectName {
			continue
		}
		if item.Labels[roleLabel] == coreDNSRole {
			coreDNSIDs = append(coreDNSIDs, item.ID)
			continue
		}
		otherProjectContainers = true
	}
	sort.Strings(coreDNSIDs)
	return coreDNSIDs, otherProjectContainers, nil
}

func (m *Manager) removeContainers(ctx context.Context, ids []string, force bool) error {
	if len(ids) == 0 {
		return nil
	}
	if _, err := m.runtime.Container.Stop(ctx, container.StopOptions{IDs: ids}); err != nil && !isNotFoundLikeError(err) {
		return fmt.Errorf("stop CoreDNS containers: %w", err)
	}
	if _, err := m.runtime.Container.Delete(ctx, container.DeleteOptions{IDs: ids, Force: force}); err != nil && !isNotFoundLikeError(err) {
		return fmt.Errorf("delete CoreDNS containers: %w", err)
	}
	return nil
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

func isNotFoundLikeError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "not found") || strings.Contains(message, "no such")
}
