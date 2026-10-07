package container

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/netip"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type ContainerClient struct {
	run          func(ctx context.Context, args ...string) (string, error)
	runStreaming func(ctx context.Context, out io.Writer, args ...string) (string, error)
}

func NewContainerClient(
	run func(ctx context.Context, args ...string) (string, error),
	runStreaming func(ctx context.Context, out io.Writer, args ...string) (string, error),
) ContainerClient {
	return ContainerClient{
		run:          run,
		runStreaming: runStreaming,
	}
}

// ListOptions defines the options for listing containers
type ListOptions struct {
	// Display both running and stopped containers
	All bool
	// Return only container IDs
	Quiet bool
}

// ContainerSummary is the information returned for a container by the list
// command.
type ContainerSummary struct {
	ID     string
	Labels map[string]string
}

// ContainerDetails is the container metadata needed by higher-level clients
// after inspecting a container.
type ContainerDetails struct {
	ID          string
	ImageDigest string
	VolumeNames []string
	Mounts      []ContainerMount
	Networks    []NetworkAttachment
}

// ContainerMount describes one configured container mount returned by inspect.
type ContainerMount struct {
	Type     MountType
	Source   string
	Target   string
	ReadOnly bool
}

// NetworkAttachment describes a container's assigned addresses on one runtime
// network. Addresses do not include the CIDR prefixes reported by the
// container CLI's inspect output.
type NetworkAttachment struct {
	Name      string
	Addresses []netip.Addr
}

// List returns a list of containers based on the provided options
func (c *ContainerClient) List(ctx context.Context, opts ListOptions) (string, error) {
	args := []string{"list"}

	// Flag parsing
	// only json output is supported
	args = append(args, "--format", "json")
	if opts.All {
		args = append(args, "--all")
	}

	out, err := c.run(ctx, args...)
	if err != nil {
		if strings.Contains(out, "failed to list containers") {
			return "", ErrSystemNotRunning
		}
	}
	return out, err
}

// ListSummaries returns typed container metadata from the list command.
func (c *ContainerClient) ListSummaries(ctx context.Context, opts ListOptions) ([]ContainerSummary, error) {
	out, err := c.List(ctx, opts)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return []ContainerSummary{}, nil
	}

	var response []struct {
		Configuration struct {
			ID     string            `json:"id"`
			Labels map[string]string `json:"labels"`
		} `json:"configuration"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		return nil, fmt.Errorf("decode container list output: %w", err)
	}

	summaries := make([]ContainerSummary, 0, len(response))
	for _, item := range response {
		if item.Configuration.ID == "" {
			continue
		}
		summaries = append(summaries, ContainerSummary{
			ID:     item.Configuration.ID,
			Labels: item.Configuration.Labels,
		})
	}
	return summaries, nil
}

// InspectDetails returns the volume and network metadata needed by
// higher-level clients. It intentionally exposes only typed fields rather
// than the runtime's full inspect response.
func (c *ContainerClient) InspectDetails(ctx context.Context, ids []string) ([]ContainerDetails, error) {
	if len(ids) == 0 {
		return nil, ErrInvalidOptions
	}

	out, err := c.run(ctx, append([]string{"inspect"}, ids...)...)
	if err != nil {
		if strings.Contains(out, "XPC connection error") {
			return nil, ErrSystemNotRunning
		}
		if out != "" {
			return nil, fmt.Errorf("inspect containers: %w: %s", err, out)
		}
		return nil, err
	}

	var response []struct {
		ID            string `json:"id"`
		Configuration struct {
			ID    string `json:"id"`
			Image struct {
				Descriptor struct {
					Digest string `json:"digest"`
				} `json:"descriptor"`
			} `json:"image"`
			Mounts []struct {
				Type        json.RawMessage `json:"type"`
				Destination string          `json:"destination"`
				Target      string          `json:"target"`
				Options     []string        `json:"options"`
			} `json:"mounts"`
		} `json:"configuration"`
		Status struct {
			Networks []struct {
				Network     string `json:"network"`
				IPv4Address string `json:"ipv4Address"`
				IPv6Address string `json:"ipv6Address"`
			} `json:"networks"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		return nil, fmt.Errorf("decode container inspect output: %w", err)
	}

	details := make([]ContainerDetails, 0, len(response))
	for _, item := range response {
		id := item.Configuration.ID
		if id == "" {
			id = item.ID
		}
		if id == "" {
			continue
		}

		volumeNames := make([]string, 0)
		var mounts []ContainerMount
		for _, mount := range item.Configuration.Mounts {
			var filesystemType struct {
				Volume struct {
					Name string `json:"name"`
				} `json:"volume"`
				Share struct {
					Source string `json:"source"`
				} `json:"share"`
				Tmpfs json.RawMessage `json:"tmpfs"`
			}
			if err := json.Unmarshal(mount.Type, &filesystemType); err != nil {
				return nil, fmt.Errorf("decode container inspect mount for %q: %w", id, err)
			}
			target := mount.Destination
			if target == "" {
				target = mount.Target
			}
			readOnly := false
			for _, option := range mount.Options {
				if option == "ro" || option == "readonly" {
					readOnly = true
					break
				}
			}
			if filesystemType.Volume.Name != "" {
				volumeNames = append(volumeNames, filesystemType.Volume.Name)
				mounts = append(mounts, ContainerMount{
					Type:     MountTypeVolume,
					Source:   filesystemType.Volume.Name,
					Target:   target,
					ReadOnly: readOnly,
				})
				continue
			}
			if filesystemType.Share.Source != "" {
				mounts = append(mounts, ContainerMount{
					Type:     MountTypeBind,
					Source:   filesystemType.Share.Source,
					Target:   target,
					ReadOnly: readOnly,
				})
				continue
			}
			if len(filesystemType.Tmpfs) > 0 && string(filesystemType.Tmpfs) != "null" {
				mounts = append(mounts, ContainerMount{
					Type:     MountTypeTmpfs,
					Target:   target,
					ReadOnly: readOnly,
				})
			}
		}
		sort.Strings(volumeNames)
		sort.Slice(mounts, func(i, j int) bool {
			if mounts[i].Target != mounts[j].Target {
				return mounts[i].Target < mounts[j].Target
			}
			if mounts[i].Type != mounts[j].Type {
				return mounts[i].Type < mounts[j].Type
			}
			return mounts[i].Source < mounts[j].Source
		})
		networks, err := decodeNetworkAttachments(item.Status.Networks)
		if err != nil {
			return nil, fmt.Errorf("decode container inspect networks for %q: %w", id, err)
		}
		details = append(details, ContainerDetails{ID: id, ImageDigest: item.Configuration.Image.Descriptor.Digest, VolumeNames: volumeNames, Mounts: mounts, Networks: networks})
	}
	return details, nil
}

// Start starts one stopped container.
func (c *ContainerClient) Start(ctx context.Context, id string) (string, error) {
	if strings.TrimSpace(id) == "" {
		return "", ErrInvalidOptions
	}
	out, err := c.run(ctx, "start", id)
	if err != nil && strings.Contains(out, "XPC connection error") {
		return out, ErrSystemNotRunning
	}
	return out, err
}

func decodeNetworkAttachments(raw []struct {
	Network     string `json:"network"`
	IPv4Address string `json:"ipv4Address"`
	IPv6Address string `json:"ipv6Address"`
}) ([]NetworkAttachment, error) {
	byName := make(map[string][]netip.Addr, len(raw))
	for _, attachment := range raw {
		name := strings.TrimSpace(attachment.Network)
		if name == "" {
			return nil, fmt.Errorf("network attachment has no network name")
		}
		for _, rawAddress := range []string{attachment.IPv4Address, attachment.IPv6Address} {
			if strings.TrimSpace(rawAddress) == "" {
				continue
			}
			address, err := inspectAddress(rawAddress)
			if err != nil {
				return nil, fmt.Errorf("network %q: %w", name, err)
			}
			byName[name] = append(byName[name], address)
		}
	}

	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	attachments := make([]NetworkAttachment, 0, len(names))
	for _, name := range names {
		addresses := byName[name]
		sort.Slice(addresses, func(i, j int) bool { return addresses[i].Compare(addresses[j]) < 0 })
		addresses = deduplicateAddresses(addresses)
		attachments = append(attachments, NetworkAttachment{Name: name, Addresses: addresses})
	}
	return attachments, nil
}

func inspectAddress(raw string) (netip.Addr, error) {
	value := strings.TrimSpace(raw)
	if prefix, err := netip.ParsePrefix(value); err == nil {
		return prefix.Addr().Unmap(), nil
	}
	address, err := netip.ParseAddr(value)
	if err != nil || address.Zone() != "" {
		return netip.Addr{}, fmt.Errorf("invalid IP address %q", raw)
	}
	return address.Unmap(), nil
}

func deduplicateAddresses(addresses []netip.Addr) []netip.Addr {
	if len(addresses) < 2 {
		return addresses
	}
	result := addresses[:1]
	for _, address := range addresses[1:] {
		if address != result[len(result)-1] {
			result = append(result, address)
		}
	}
	return result
}

// CreateOptions defines the options for creating a container
type CreateOptions struct {
	// Name of the container to create
	Name string
	// Number of CPUs to allocate to the container
	CPUs uint
	// Amount of memory (1MiByte granularity), with optional K, M, G, T, or P suffix
	Memory string
	// Remove the container after it exits
	Rm bool
	// Port mappings
	Publish []PortMapping
	// Environment variables in KEY=VALUE (or KEY) format.
	Environment []string
	// Labels associates metadata with the container as KEY=VALUE pairs.
	Labels map[string]string
	// Nameservers identifies DNS server IP addresses to pass through repeated
	// --dns flags. Their order is preserved.
	Nameservers []string
	// Networks identifies the networks to attach when creating the container.
	Networks []string
	// Mounts configures filesystem mounts for the container.
	Mounts []Mount
	// Container init Process arguments, if any
	Arguments []string
}

// MountType identifies the storage backing used by a container mount.
type MountType string

const (
	// MountTypeBind shares a host directory with a container.
	MountTypeBind MountType = "bind"
	// MountTypeVolume attaches a named volume to a container.
	MountTypeVolume MountType = "volume"
	// MountTypeTmpfs attaches an in-memory filesystem to a container.
	MountTypeTmpfs MountType = "tmpfs"
)

// Mount configures one filesystem mount for a container.
type Mount struct {
	// Type identifies the storage backing for the mount.
	Type MountType
	// Source is the absolute host path for a bind mount or a volume name for a
	// named-volume mount. Tmpfs mounts do not have a source.
	Source string
	// Target is the absolute path where the mount appears in the container.
	Target string
	// ReadOnly prevents writes through the mount.
	ReadOnly bool
	// TmpfsSize is the tmpfs capacity in bytes. Zero uses the runtime default.
	TmpfsSize uint64
	// TmpfsMode is the tmpfs file mode expressed as an octal Unix-permission
	// string. An empty value uses the runtime default.
	TmpfsMode string
}

// PortMapping defines a mapping from a host port to a container port as taken by the container cli
type PortMapping struct {
	HostIP        string
	HostPort      uint16
	ContainerPort uint16
	Protocol      Protocol // "tcp" or "udp"
}

// Protocol types accepted by the container cli defined in the container package: TCP and UDP
type Protocol string

const (
	TCP Protocol = "tcp"
	UDP Protocol = "udp"
)

// Run creates and starts a new container based on the provided image and options. Returns true if the container was successfully created and started, or an error if the operation fails.
func (c *ContainerClient) Run(ctx context.Context, image string, opts CreateOptions) (bool, error) {
	args, err := runArgs(image, opts)
	if err != nil {
		return false, err
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		if strings.Contains(out, "XPC connection error") {
			return false, ErrSystemNotRunning
		} else if strings.Contains(out, "already exists") {
			return false, ErrContainerNameExists
		}
	}
	return true, err
}

// ValidateRunOptions checks the same create-time arguments that Run passes to
// the external CLI, without starting a container.
func ValidateRunOptions(image string, opts CreateOptions) error {
	_, err := runArgs(image, opts)
	return err
}

func runArgs(image string, opts CreateOptions) ([]string, error) {
	if strings.TrimSpace(image) == "" {
		return nil, ErrInvalidOptions
	}
	args := []string{"run"}

	if opts.Name != "" {
		args = append(args, "--name", opts.Name)
	}
	if opts.CPUs > 0 {
		args = append(args, "--cpus", strconv.Itoa(int(opts.CPUs)))
	}
	if opts.Memory != "" {
		size := opts.Memory[:len(opts.Memory)-1]
		suffix := opts.Memory[len(opts.Memory)-1]
		if suffix != 'K' && suffix != 'M' && suffix != 'G' && suffix != 'T' && suffix != 'P' {
			return nil, ErrInvalidOptions
		}
		amount, err := strconv.ParseUint(size, 10, 64)
		if err != nil {
			return nil, ErrInvalidOptions
		}
		minimum := uint64(1)
		switch suffix {
		case 'K':
			minimum = 200 * 1024
		case 'M':
			minimum = 200
		}
		if amount < minimum {
			return nil, fmt.Errorf("%w: memory %q is below Apple Container's 200 MiB minimum", ErrInvalidOptions, opts.Memory)
		}
		args = append(args, "--memory", opts.Memory)
	}
	if opts.Rm {
		args = append(args, "--rm")
	}
	nameservers, err := NormalizeNameservers(opts.Nameservers)
	if err != nil {
		return nil, err
	}
	for _, nameserver := range nameservers {
		args = append(args, "--dns", nameserver)
	}
	if len(opts.Environment) > 0 {
		for _, entry := range opts.Environment {
			if strings.TrimSpace(entry) == "" {
				continue
			}
			args = append(args, "--env", entry)
		}
	}
	if len(opts.Labels) > 0 {
		keys := make([]string, 0, len(opts.Labels))
		for key := range opts.Labels {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if strings.TrimSpace(key) == "" {
				return nil, ErrInvalidOptions
			}
			args = append(args, "--label", key+"="+opts.Labels[key])
		}
	}
	for _, networkName := range opts.Networks {
		networkName = strings.TrimSpace(networkName)
		if networkName == "" {
			return nil, ErrInvalidOptions
		}
		args = append(args, "--network", networkName)
	}
	for _, mount := range opts.Mounts {
		mountSpec, err := mount.spec()
		if err != nil {
			return nil, err
		}
		args = append(args, "--mount", mountSpec)
	}
	if opts.Publish != nil {
		for _, mapping := range opts.Publish {
			portMapping := ""
			if mapping.HostIP != "" {
				ip := net.ParseIP(mapping.HostIP) // Validate the IP address
				if ip == nil || ip.To4() == nil {
					return nil, ErrInvalidOptions
				}
				portMapping += ip.String() + ":"
			}
			portMapping += strconv.Itoa(int(mapping.HostPort)) + ":" + strconv.Itoa(int(mapping.ContainerPort))
			if mapping.Protocol != "" {
				portMapping += "/" + string(mapping.Protocol)
			}
			args = append(args, "-p", portMapping)
		}
	}
	// To run the container detached
	args = append(args, "-d")
	args = append(args, image)
	args = append(args, opts.Arguments...)
	return args, nil
}

// NormalizeNameservers validates literal DNS server addresses and returns their
// canonical string forms. Loopback, unspecified, multicast, and link-local
// addresses cannot identify a nameserver reachable from another container.
func NormalizeNameservers(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	nameservers := make([]string, 0, len(raw))
	for _, value := range raw {
		address, err := netip.ParseAddr(strings.TrimSpace(value))
		if err != nil || address.Zone() != "" {
			return nil, fmt.Errorf("%w: invalid nameserver %q", ErrInvalidOptions, value)
		}
		address = address.Unmap()
		if address.IsLoopback() || address.IsUnspecified() || address.IsMulticast() || address.IsLinkLocalUnicast() {
			return nil, fmt.Errorf("%w: unusable nameserver %q", ErrInvalidOptions, value)
		}
		nameservers = append(nameservers, address.String())
	}
	return nameservers, nil
}

func (m Mount) spec() (string, error) {
	if !filepath.IsAbs(m.Target) {
		return "", ErrInvalidOptions
	}
	if m.Type == MountTypeTmpfs {
		if m.Source != "" {
			return "", ErrInvalidOptions
		}
		spec := "type=" + string(m.Type) + ",target=" + m.Target
		if m.TmpfsSize > 0 {
			spec += ",size=" + strconv.FormatUint(m.TmpfsSize, 10)
		}
		if m.TmpfsMode != "" {
			spec += ",mode=" + m.TmpfsMode
		}
		if m.ReadOnly {
			spec += ",readonly"
		}
		return spec, nil
	}
	if m.Source == "" {
		return "", ErrInvalidOptions
	}
	if m.Type == MountTypeBind && !filepath.IsAbs(m.Source) {
		return "", ErrInvalidOptions
	}
	if m.Type != MountTypeBind && m.Type != MountTypeVolume {
		return "", ErrInvalidOptions
	}

	spec := "type=" + string(m.Type) + ",source=" + m.Source + ",target=" + m.Target
	if m.ReadOnly {
		spec += ",readonly"
	}
	return spec, nil
}

// LogsOptions defines options for streaming or fetching container logs.
type LogsOptions struct {
	// Follow streams logs continuously until context cancellation or process exit.
	Follow bool
	// IDs are container IDs or names to fetch logs for.
	IDs []string
}

// Logs streams container logs to the provided writer.
func (c *ContainerClient) Logs(ctx context.Context, opts LogsOptions, out io.Writer) (string, error) {
	if len(opts.IDs) == 0 {
		return "", ErrInvalidOptions
	}

	args := []string{"logs"}
	if opts.Follow {
		args = append(args, "--follow")
	}
	args = append(args, opts.IDs...)

	if c.runStreaming != nil {
		logsOut, err := c.runStreaming(ctx, out, args...)
		if err != nil && strings.Contains(logsOut, "XPC connection error") {
			return logsOut, ErrSystemNotRunning
		}
		return logsOut, err
	}

	logsOut, err := c.run(ctx, args...)
	if out != nil && logsOut != "" {
		_, _ = out.Write([]byte(logsOut))
	}
	if err != nil && strings.Contains(logsOut, "XPC connection error") {
		return logsOut, ErrSystemNotRunning
	}
	return logsOut, err
}

// StopOptions defines the options for stopping containers
type StopOptions struct {
	// Stop all running containers
	All bool
	// List of container IDs or names to stop, should be empty if All flag is true
	IDs []string
	// Seconds to wait before killing the containers (default: 5)
	Time uint
	// Signal to send to the containers (default: SIGTERM)
	Signal Signal
}

// Signal defines the signal types accepted by the container cli defined under container package
type Signal string

const (
	SIGTERM Signal = "SIGTERM"
	SIGKILL Signal = "SIGKILL"
	SIGINT  Signal = "SIGINT"
	SIGQUIT Signal = "SIGQUIT"
	SIGUSR1 Signal = "SIGUSR1"
	SIGUSR2 Signal = "SIGUSR2"
)

// Stop stops one or more running containers based on the provided options. Returns the ID of the stopped container(s) and an error if the operation fails.
func (c *ContainerClient) Stop(ctx context.Context, opts StopOptions) (string, error) {
	args := []string{}
	if opts.Signal != "" {
		args = append(args, "--signal", string(opts.Signal))
	}
	if opts.Time > 0 {
		args = append(args, "--time", strconv.Itoa(int(opts.Time)))
	}
	if opts.All {
		if len(opts.IDs) > 0 {
			return "", ErrInvalidOptions
		}
		out, err := c.run(ctx, append([]string{"stop", "--all"}, args...)...)
		if err != nil {
			return out, err
		}
		return out, nil
	}
	if len(opts.IDs) == 0 {
		return "", ErrInvalidOptions
	}
	args = append(args, opts.IDs...)
	args = append([]string{"stop"}, args...)
	out, err := c.run(ctx, args...)
	if err != nil {
		return out, err
	}
	return out, nil
}

type DeleteOptions struct {
	// Delete all containers
	All bool
	// List of container IDs or names to delete, should be empty if All flag is true
	IDs []string
	// Force deletion of running containers
	Force bool
}

// Delete deletes one or more containers based on the provided options. Returns the ID of the deleted container(s) and an error if the operation fails.
func (c *ContainerClient) Delete(ctx context.Context, opts DeleteOptions) (string, error) {
	args := []string{}
	if opts.Force {
		args = append(args, "--force")
	}
	if opts.All {
		if len(opts.IDs) > 0 {
			return "", ErrInvalidOptions
		}
		out, err := c.run(ctx, append([]string{"delete", "--all"}, args...)...)
		if err != nil {
			return out, err
		}
		return out, nil
	}
	if len(opts.IDs) == 0 {
		return "", ErrInvalidOptions
	}
	args = append(args, opts.IDs...)
	args = append([]string{"delete"}, args...)
	out, err := c.run(ctx, args...)
	if err != nil {
		return out, err
	}
	return out, nil
}
