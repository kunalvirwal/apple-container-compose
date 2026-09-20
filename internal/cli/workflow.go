package cli

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/kunalvirwal/apple-container-compose/internal/coredns"
	"github.com/kunalvirwal/apple-container-compose/internal/state"
	"github.com/kunalvirwal/apple-container-compose/pkg/compose"
	"github.com/kunalvirwal/apple-container-compose/pkg/container"
)

type dnsManager interface {
	Start(context.Context, string, []string) (map[string]netip.Addr, error)
}

// runUp starts the services defined by a Compose file, optionally configuring ACC-managed CoreDNS infrastructure.
func runUp(ctx context.Context, path string, parseOpts compose.ParseOptions, opts compose.UpOptions, noCoreDNS bool) error {
	runtime, err := container.NewClient()
	if err != nil {
		return err
	}
	statePath, err := state.DefaultPath()
	if err != nil {
		return err
	}
	registry, err := newStateServiceRegistry(statePath)
	if err != nil {
		return err
	}
	composeClient, err := compose.NewComposeClient(
		compose.WithContainerClient(runtime),
		compose.WithServiceRegistry(registry),
	)
	if err != nil {
		return err
	}
	if noCoreDNS {
		return composeClient.Up(ctx, path, parseOpts, opts)
	}
	coreDNSManager, err := coredns.NewManager(runtime, coredns.ManagerOptions{StatePath: statePath})
	if err != nil {
		return err
	}
	return startServicesWithCoreDNS(ctx, composeClient, coreDNSManager, path, parseOpts, opts)
}

// runDown stops and removes the services and resources defined by a Compose file, cleaning up project CoreDNS infrastructure when appropriate.
func runDown(ctx context.Context, path string, parseOpts compose.ParseOptions, opts compose.DownOptions) error {
	runtime, err := container.NewClient()
	if err != nil {
		return err
	}
	statePath, err := state.DefaultPath()
	if err != nil {
		return err
	}
	registry, err := newStateServiceRegistry(statePath)
	if err != nil {
		return err
	}
	composeClient, err := compose.NewComposeClient(
		compose.WithContainerClient(runtime),
		compose.WithServiceRegistry(registry),
	)
	if err != nil {
		return err
	}
	session, err := composeClient.PrepareDown(ctx, path, parseOpts, opts)
	if err != nil {
		return err
	}
	if err := session.RemoveServices(ctx); err != nil {
		return err
	}
	if len(opts.Services) == 0 {
		coreDNSManager, err := coredns.NewManager(runtime, coredns.ManagerOptions{StatePath: statePath})
		if err != nil {
			return err
		}
		if err := coreDNSManager.RemoveIfUnused(ctx, session.ProjectName(), opts.Force); err != nil {
			return err
		}
	}
	return session.RemoveResources(ctx)
}

// startServicesWithCoreDNS coordinates ACC-owned DNS infrastructure around the
// generic Compose startup phases. The Compose SDK remains DNS-implementation
// agnostic.
func startServicesWithCoreDNS(ctx context.Context, composeClient *compose.ComposeClient, dns dnsManager, path string, parseOpts compose.ParseOptions, opts compose.UpOptions) error {
	if composeClient == nil {
		return fmt.Errorf("up has no Compose client")
	}
	if dns == nil {
		return fmt.Errorf("up has no DNS manager")
	}

	session, err := composeClient.PrepareUp(ctx, path, parseOpts, opts)
	if err != nil {
		return err
	}
	dnsByNetwork, err := dns.Start(ctx, session.ProjectName(), session.NetworkNames())
	if err != nil {
		return err
	}
	return session.StartServices(ctx, compose.ServiceStartOptions{DNSByNetwork: dnsByNetwork})
}
