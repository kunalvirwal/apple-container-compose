package accplugin

import (
	"net/netip"
	"slices"
)

// ResolutionKind describes whether ACC owns and can answer a query.
type ResolutionKind uint8

const (
	// ResolutionUnmanaged means the name is not in ACC state and should be
	// handed to the next CoreDNS plugin, normally forward.
	ResolutionUnmanaged ResolutionKind = iota
	// ResolutionAnswer contains one network-scoped record set.
	ResolutionAnswer
	// ResolutionHidden means ACC owns the name but it is not visible to the
	// requester on any shared network.
	ResolutionHidden
	// ResolutionUnknownRequester means ACC owns the name, but has no network
	// memberships for this source IP. Keep this separate from external names
	// so the handler's policy can be chosen independently.
	ResolutionUnknownRequester
)

// Resolution is the CoreDNS-independent result of a source-aware lookup.
type Resolution struct {
	Kind      ResolutionKind
	Addresses []netip.Addr
}

// Resolve applies ACC's network-visibility rule to sourceIP and name.
func (r *FileRegistry) Resolve(sourceIP netip.Addr, name string) Resolution {
	s := r.current.Load()
	if s == nil {
		return Resolution{Kind: ResolutionUnmanaged}
	}
	normalizedName, err := normalizeName(name)
	if err != nil {
		return Resolution{Kind: ResolutionUnmanaged}
	}
	if _, managed := s.managedName[normalizedName]; !managed {
		return Resolution{Kind: ResolutionUnmanaged}
	}
	networks, knownRequester := s.bySourceIP[sourceIP.Unmap()]
	if !knownRequester {
		return Resolution{Kind: ResolutionUnknownRequester}
	}

	// All requester networks are sorted at load time, independently of which
	// interface sent the packet. Choose the first network containing the name;
	// return all replicas on that network. This is ACC's deterministic tie-break,
	// not an implementation of Docker's endpoint-priority ordering.
	for _, network := range networks {
		records := s.byNetwork[network][normalizedName]
		if len(records) == 0 {
			continue
		}
		return Resolution{Kind: ResolutionAnswer, Addresses: slices.Clone(records)}
	}
	return Resolution{Kind: ResolutionHidden}
}
