package cli

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"github.com/kunalvirwal/apple-container-compose/internal/coredns"
	"github.com/kunalvirwal/apple-container-compose/internal/state"
	"github.com/kunalvirwal/apple-container-compose/pkg/compose"
	"github.com/kunalvirwal/apple-container-compose/pkg/container"
)

type dnsManager interface {
	Start(context.Context, string, []string) (map[string]netip.Addr, error)
	FinalizeMigration(context.Context, string, []string) (bool, error)
}

type dnsRemover interface {
	RemoveIfUnused(context.Context, string, bool) error
}

type directDNSRemover interface {
	RemoveAfterDirectDNS(context.Context, string, []string) (bool, error)
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
	coreDNSManager, err := coredns.NewManager(runtime, coredns.ManagerOptions{StatePath: statePath})
	if err != nil {
		return err
	}
	reporter := newUpReporter(runtime, opts.Output)
	if noCoreDNS {
		return startServicesWithoutCoreDNS(ctx, composeClient, coreDNSManager, path, parseOpts, opts, reporter)
	}
	return startServicesWithCoreDNS(ctx, composeClient, coreDNSManager, path, parseOpts, opts, reporter)
}

// startServicesWithoutCoreDNS reconciles services with Compose DNS before
// retiring ACC-owned DNS infrastructure and obsolete project networks.
func startServicesWithoutCoreDNS(ctx context.Context, composeClient *compose.ComposeClient, dns directDNSRemover, path string, parseOpts compose.ParseOptions, opts compose.UpOptions, reporters ...*upReporter) error {
	if composeClient == nil {
		return fmt.Errorf("up has no Compose client")
	}
	if dns == nil {
		return fmt.Errorf("up has no CoreDNS manager")
	}
	var reporter *upReporter
	if len(reporters) > 0 {
		reporter = reporters[0]
	}
	reporter.line("Preparing Compose project with direct DNS (--no-coredns)")
	beforeResources, beforeRecorded := reporter.capture(ctx, "", true, true, false)
	session, err := composeClient.PrepareUp(ctx, path, parseOpts, opts)
	if err != nil {
		return err
	}
	prepared, preparedRecorded := reporter.capture(ctx, session.ProjectName(), true, true, true)
	if beforeRecorded && preparedRecorded {
		reporter.prepared(session.ProjectName(), session.NetworkNames(), beforeResources, prepared)
	}
	if err := session.StartServices(ctx, compose.ServiceStartOptions{}); err != nil {
		return err
	}
	defer session.StopLogs()
	cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancelCleanup()
	started, startedRecorded := reporter.capture(cleanupCtx, session.ProjectName(), false, false, true)
	if preparedRecorded && startedRecorded {
		reporter.services(session.ServiceNames(), opts.Build, opts.RenewAnonymousVolumes, prepared, started)
	}
	removed, err := dns.RemoveAfterDirectDNS(cleanupCtx, session.ProjectName(), session.ServiceNames())
	if err != nil {
		return err
	}
	retired, retiredRecorded := reporter.capture(cleanupCtx, session.ProjectName(), false, false, true)
	if startedRecorded && retiredRecorded {
		reporter.coreDNSFinished(started, retired, true)
	}
	if !removed {
		if retiredRecorded {
			reporter.line("Keeping earlier project networks - unselected or orphaned containers may still need CoreDNS")
		} else if opts.OnWarning != nil {
			opts.OnWarning("retaining project CoreDNS and its networks because unselected or orphaned project containers may still use them")
		}
		return session.WaitForLogs()
	}
	if err := session.RemoveObsoleteNetworks(cleanupCtx); err != nil {
		return err
	}
	cleaned, cleanedRecorded := reporter.capture(cleanupCtx, session.ProjectName(), true, false, false)
	if preparedRecorded && cleanedRecorded {
		reporter.networksRemoved(session.ProjectName(), prepared, cleaned)
	}
	return session.WaitForLogs()
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
	if len(opts.Services) > 0 {
		return composeClient.Down(ctx, path, parseOpts, opts)
	}
	coreDNSManager, err := coredns.NewManager(runtime, coredns.ManagerOptions{StatePath: statePath})
	if err != nil {
		return err
	}
	return downServicesWithCoreDNS(ctx, composeClient, coreDNSManager, path, parseOpts, opts)
}

// downServicesWithCoreDNS coordinates service and project-resource teardown
// around ACC-owned DNS infrastructure. Volume removal precedes DNS removal so
// a complete teardown occurs in service, volume, DNS, then network order.
func downServicesWithCoreDNS(ctx context.Context, composeClient *compose.ComposeClient, dns dnsRemover, path string, parseOpts compose.ParseOptions, opts compose.DownOptions) error {
	if composeClient == nil {
		return fmt.Errorf("down has no Compose client")
	}
	if dns == nil {
		return fmt.Errorf("down has no CoreDNS manager")
	}

	session, err := composeClient.PrepareDown(ctx, path, parseOpts, opts)
	if err != nil {
		return err
	}
	if err := session.RemoveServices(ctx); err != nil {
		return err
	}
	if err := session.RemoveVolumes(ctx); err != nil {
		return err
	}
	if len(opts.Services) == 0 {
		if err := dns.RemoveIfUnused(ctx, session.ProjectName(), opts.Force); err != nil {
			return err
		}
	}
	return session.RemoveNetworks(ctx)
}

// startServicesWithCoreDNS coordinates ACC-owned DNS infrastructure around the
// generic Compose startup phases. The Compose SDK remains DNS-implementation
// agnostic.
func startServicesWithCoreDNS(ctx context.Context, composeClient *compose.ComposeClient, dns dnsManager, path string, parseOpts compose.ParseOptions, opts compose.UpOptions, reporters ...*upReporter) error {
	if composeClient == nil {
		return fmt.Errorf("up has no Compose client")
	}
	if dns == nil {
		return fmt.Errorf("up has no DNS manager")
	}
	var reporter *upReporter
	if len(reporters) > 0 {
		reporter = reporters[0]
	}
	reporter.line("Preparing Compose project with ACC CoreDNS")
	beforeResources, beforeRecorded := reporter.capture(ctx, "", true, true, false)

	session, err := composeClient.PrepareUp(ctx, path, parseOpts, opts)
	if err != nil {
		return err
	}
	prepared, preparedRecorded := reporter.capture(ctx, session.ProjectName(), true, true, true)
	if beforeRecorded && preparedRecorded {
		reporter.prepared(session.ProjectName(), session.NetworkNames(), beforeResources, prepared)
	}
	dnsByNetwork, err := dns.Start(ctx, session.ProjectName(), session.NetworkNames())
	if err != nil {
		return err
	}
	dnsStarted, dnsRecorded := reporter.capture(ctx, session.ProjectName(), false, false, true)
	if preparedRecorded && dnsRecorded {
		reporter.coreDNSStarted(session.NetworkNames(), prepared, dnsStarted)
	}
	if err := session.StartServices(ctx, compose.ServiceStartOptions{DNSByNetwork: dnsByNetwork}); err != nil {
		return err
	}
	defer session.StopLogs()
	cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancelCleanup()
	started, startedRecorded := reporter.capture(cleanupCtx, session.ProjectName(), false, false, true)
	if dnsRecorded && startedRecorded {
		reporter.services(session.ServiceNames(), opts.Build, opts.RenewAnonymousVolumes, dnsStarted, started)
	}
	retiredOlderDNS, err := dns.FinalizeMigration(cleanupCtx, session.ProjectName(), session.ServiceNames())
	if err != nil {
		return err
	}
	retired, retiredRecorded := reporter.capture(cleanupCtx, session.ProjectName(), false, false, true)
	if startedRecorded && retiredRecorded {
		reporter.coreDNSFinished(started, retired, false)
	}
	if !retiredOlderDNS {
		if retiredRecorded {
			reporter.line("Keeping earlier project networks - unselected or orphaned containers may still need old CoreDNS")
		} else if opts.OnWarning != nil {
			opts.OnWarning("retaining earlier CoreDNS containers and their networks because unselected or orphaned project containers may still use them")
		}
		return session.WaitForLogs()
	}
	if err := session.RemoveObsoleteNetworks(cleanupCtx); err != nil {
		return err
	}
	cleaned, cleanedRecorded := reporter.capture(cleanupCtx, session.ProjectName(), true, false, false)
	if preparedRecorded && cleanedRecorded {
		reporter.networksRemoved(session.ProjectName(), prepared, cleaned)
	}
	return session.WaitForLogs()
}
