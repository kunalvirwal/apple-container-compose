package accplugin

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"unicode"
)

// State is the host-global registry written by ACC.
type State struct {
	Version    int         `json:"version"`
	Containers []Container `json:"containers"`
}

// Container groups all addresses and networks of one running container.
// Replicas have distinct IDs and may share a service name.
type Container struct {
	ID          string                       `json:"id"`
	Service     string                       `json:"service"`
	Nameservers []string                     `json:"nameservers,omitempty"`
	Networks    map[string]NetworkAttachment `json:"networks"`
}

// NetworkAttachment holds addresses and aliases on a runtime network. Its map
// key is the runtime network ID/name, never a project's logical network key.
// The service name is registered automatically alongside these aliases.
type NetworkAttachment struct {
	Addresses []string `json:"addresses"`
	Aliases   []string `json:"aliases,omitempty"`
}

type snapshot struct {
	bySourceIP  map[netip.Addr]requester // every network and DNS configuration of the requester
	byNetwork   map[string]map[string][]netip.Addr
	managedName map[string]struct{}
}

// requester holds immutable information selected by a DNS query's source
// address. Nameservers preserve their configured ordering.
type requester struct {
	networks    []string
	nameservers []netip.Addr
}

func buildSnapshot(state State) (*snapshot, error) {
	if state.Version != 1 {
		return nil, fmt.Errorf("unsupported state version %d", state.Version)
	}
	if state.Containers == nil {
		return nil, fmt.Errorf("containers must be an array; use [] for an empty registry")
	}
	s := &snapshot{
		bySourceIP:  make(map[netip.Addr]requester),
		byNetwork:   make(map[string]map[string][]netip.Addr),
		managedName: make(map[string]struct{}),
	}
	ids := make(map[string]bool)
	addressOwner := make(map[netip.Addr]string)
	for _, container := range state.Containers {
		if !validID(container.ID) {
			return nil, fmt.Errorf("container id must be nonempty and contain no whitespace/control characters")
		}
		if ids[container.ID] {
			return nil, fmt.Errorf("duplicate container id %q", container.ID)
		}
		ids[container.ID] = true
		service, err := normalizeName(container.Service)
		if err != nil {
			return nil, fmt.Errorf("container %q service: %w", container.ID, err)
		}
		if len(container.Networks) == 0 {
			return nil, fmt.Errorf("container %q has no networks", container.ID)
		}
		nameservers, err := parseNameservers(container.Nameservers)
		if err != nil {
			return nil, fmt.Errorf("container %q nameservers: %w", container.ID, err)
		}
		networks := make([]string, 0, len(container.Networks))
		for network := range container.Networks {
			networks = append(networks, network)
		}
		slices.Sort(networks)
		for _, network := range networks {
			if !validID(network) {
				return nil, fmt.Errorf("container %q has invalid network id %q", container.ID, network)
			}
			attachment := container.Networks[network]
			addresses, err := attachmentAddresses(attachment)
			if err != nil {
				return nil, fmt.Errorf("container %q network %q: %w", container.ID, network, err)
			}
			for _, address := range addresses {
				if owner, exists := addressOwner[address]; exists && owner != container.ID {
					return nil, fmt.Errorf("address %s belongs to containers %q and %q; source-IP identification requires unique addresses across containers", address, owner, container.ID)
				}
				addressOwner[address] = container.ID
				s.bySourceIP[address] = requester{networks: networks, nameservers: nameservers}
			}
			names := []string{service}
			for _, alias := range attachment.Aliases {
				name, err := normalizeName(alias)
				if err != nil {
					return nil, fmt.Errorf("container %q network %q alias: %w", container.ID, network, err)
				}
				names = append(names, name)
			}
			if s.byNetwork[network] == nil {
				s.byNetwork[network] = make(map[string][]netip.Addr)
			}
			slices.Sort(names)
			for _, name := range slices.Compact(names) {
				s.managedName[name] = struct{}{}
				s.byNetwork[network][name] = append(s.byNetwork[network][name], addresses...)
			}
		}
	}
	for _, records := range s.byNetwork {
		for name, addresses := range records {
			records[name] = uniqueAddresses(addresses)
		}
	}
	return s, nil
}

func attachmentAddresses(attachment NetworkAttachment) ([]netip.Addr, error) {
	if len(attachment.Addresses) == 0 {
		return nil, fmt.Errorf("network attachment has no addresses")
	}
	addresses := make([]netip.Addr, 0, len(attachment.Addresses))
	for _, raw := range attachment.Addresses {
		address, err := netip.ParseAddr(raw)
		if err != nil || address.Zone() != "" {
			return nil, fmt.Errorf("invalid unscoped IP address %q", raw)
		}
		address = address.Unmap()
		if !address.IsGlobalUnicast() {
			return nil, fmt.Errorf("address %q must be a unicast container address (private IPv4 and IPv6 ULA are allowed)", raw)
		}
		addresses = append(addresses, address)
	}
	return uniqueAddresses(addresses), nil
}

func parseNameservers(raw []string) ([]netip.Addr, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	nameservers := make([]netip.Addr, 0, len(raw))
	for _, value := range raw {
		address, err := netip.ParseAddr(value)
		if err != nil || address.Zone() != "" {
			return nil, fmt.Errorf("invalid unscoped IP address %q", value)
		}
		address = address.Unmap()
		if !address.IsGlobalUnicast() {
			return nil, fmt.Errorf("nameserver %q must be a unicast address", value)
		}
		nameservers = append(nameservers, address)
	}
	return nameservers, nil
}

// normalizeName allows Compose-style underscores and optional dotted names,
// but no empty/oversized labels, whitespace, escapes, wildcards or non-ASCII
// characters. A future search-suffix policy can be applied separately.
func normalizeName(raw string) (string, error) {
	for _, c := range raw {
		if c > unicode.MaxASCII {
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
		for _, c := range label {
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
				return "", fmt.Errorf("invalid DNS name %q", raw)
			}
		}
	}
	return name, nil
}

func validID(id string) bool {
	return id != "" && !strings.ContainsFunc(id, func(c rune) bool { return unicode.IsSpace(c) || unicode.IsControl(c) })
}

func uniqueAddresses(addresses []netip.Addr) []netip.Addr {
	slices.SortFunc(addresses, func(a, b netip.Addr) int { return a.Compare(b) })
	return slices.Compact(addresses)
}
