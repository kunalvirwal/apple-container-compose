package compose

import (
	"context"
	"net/netip"
)

// ServiceRegistry receives runtime service records created and removed by
// Compose. Implementations may persist these records or publish them to a
// service-discovery system. Omitting WithServiceRegistry disables this
// integration.
type ServiceRegistry interface {
	// Ensure prepares the registry before Compose mutates runtime resources.
	Ensure(context.Context) error
	// Upsert records a service after its container has been started and inspected.
	// Its errors are reported as warnings because the runtime has already changed.
	Upsert(context.Context, ServiceRuntime) error
	// Remove deletes records after the corresponding containers are removed. Its
	// errors are reported as warnings and do not interrupt runtime cleanup.
	Remove(context.Context, []string) error
}

// ServiceRuntime describes one running Compose service container.
type ServiceRuntime struct {
	// ProjectName is the resolved Compose project name.
	ProjectName string
	// ServiceName is the service's key in the Compose project.
	ServiceName string
	// ContainerID is the runtime container identifier.
	ContainerID string
	// Nameservers contains the service's resolved Compose dns values.
	Nameservers []string
	// Networks contains the container's inspected network attachments.
	Networks []ServiceNetworkRuntime
}

// ServiceNetworkRuntime describes a service container's addresses and aliases
// on one runtime network.
type ServiceNetworkRuntime struct {
	// Name is the Compose-resolved runtime network name.
	Name string
	// Addresses contains assigned IP addresses without CIDR prefixes.
	Addresses []netip.Addr
	// Aliases contains aliases declared for this service on this network.
	Aliases []string
}
