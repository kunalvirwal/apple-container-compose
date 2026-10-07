package compose

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/kunalvirwal/apple-container-compose/pkg/container"
)

// UpOptions controls compose up behavior.
type UpOptions struct {
	// Services limits startup to the selected services. Empty means all services.
	Services []string
	// Build forces all buildable services to build before any containers start.
	// Without it, a buildable service is built only when its local image is absent.
	Build bool
	// RenewAnonymousVolumes creates fresh anonymous volumes when existing
	// service containers are recreated instead of reusing volumes by target.
	RenewAnonymousVolumes bool
	// RemoveOrphans removes ACC-owned containers for services that are no
	// longer declared by the loaded Compose project.
	RemoveOrphans bool
	// Output receives build output and attached service logs when enabled.
	Output io.Writer
	// BuildOutput receives build output. When nil, Output is used.
	BuildOutput io.Writer
	// Attach streams service logs until detached or context cancellation.
	Attach bool
	// Detach, when non-nil, detaches attached log streaming when signaled.
	// Canceling the context also detaches.
	Detach <-chan struct{}
	// OnWarning receives non-fatal Compose warnings. The SDK never renders
	// warnings itself; callers decide whether and how to present them.
	OnWarning func(string)
	// OnFatalWarning receives a diagnostic immediately before Up returns an
	// error for an unsupported Compose feature. The SDK never renders
	// diagnostics itself; callers decide whether and how to present them.
	OnFatalWarning func(string)
}

// ServiceStartOptions controls the service-start phase of an UpSession.
type ServiceStartOptions struct {
	// DNSByNetwork maps runtime network names to DNS server addresses. Every
	// network used by a service must have an entry when this is set.
	// With no entries, Compose dns values are passed directly to the runtime.
	DNSByNetwork map[string]netip.Addr
}

// UpSession is an opaque, validated Compose startup session. It is returned
// only by PrepareUp, which ensures callers cannot start services before the
// Compose project and its runtime resources have been prepared.
type UpSession struct {
	client               *ComposeClient
	project              *types.Project
	services             []string
	opts                 UpOptions
	serviceNetworks      map[string][]string
	anonymousVolumes     map[string][]string
	imageIDs             map[string]string
	directPlans          []servicePlan
	logSession           *serviceLogSession
	existingServices     map[string]existingService
	runningContainerIDs  map[string]struct{}
	orphanContainerIDs   []string
	selectedNetworkNames []string
	runtimeNetworkNames  []string
	managedNetworkNames  []string
	obsoleteNetworkNames []string
}

type existingService struct {
	summary container.ContainerSummary
	details container.ContainerDetails
}

type serviceAction uint8

const (
	serviceCreate serviceAction = iota
	serviceReuse
	serviceStart
	serviceRecreate
)

type servicePlan struct {
	serviceName string
	service     types.ServiceConfig
	image       string
	name        string
	options     container.CreateOptions
	existingID  string
	action      serviceAction
}

// ProjectName returns the resolved project name for this startup session.
func (p *UpSession) ProjectName() string {
	if p == nil || p.project == nil {
		return ""
	}
	return p.project.Name
}

// NetworkNames returns the sorted runtime networks required by selected
// services and by existing project containers that this Up leaves untouched.
func (p *UpSession) NetworkNames() []string {
	if p == nil {
		return nil
	}
	return append([]string(nil), p.runtimeNetworkNames...)
}

// Up brings selected services up in dependency order. It builds missing local
// images for services that declare build configuration before starting them.
func (c *ComposeClient) Up(ctx context.Context, path string, parseOpts ParseOptions, opts UpOptions) error {
	session, err := c.PrepareUp(ctx, path, parseOpts, opts)
	if err != nil {
		return err
	}
	if err := session.StartServices(ctx, ServiceStartOptions{}); err != nil {
		return err
	}
	cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancelCleanup()
	if err := session.RemoveObsoleteNetworks(cleanupCtx); err != nil {
		session.StopLogs()
		return err
	}
	return session.WaitForLogs()
}

// PrepareUp loads and validates a Compose project, resolves images and mount
// sources, creates required volumes, and ensures required networks. It does
// not start any Compose service containers.
func (c *ComposeClient) PrepareUp(ctx context.Context, path string, parseOpts ParseOptions, opts UpOptions) (*UpSession, error) {
	if c.containerClient == nil {
		return nil, ErrContainerClientNil
	}

	project, err := c.loadProject(ctx, path, parseOpts)
	if err != nil {
		return nil, err
	}

	buildOutput := opts.BuildOutput
	if buildOutput == nil {
		buildOutput = opts.Output
	}

	services, err := generateServiceOrder(project, opts.Services)
	if err != nil {
		return nil, err
	}
	networkDefinitions, err := validateNetworks(project, services, opts.OnFatalWarning)
	if err != nil {
		return nil, err
	}
	if err := validateServiceMounts(project, services, opts.OnWarning, opts.OnFatalWarning); err != nil {
		return nil, err
	}
	if err := validateProjectVolumeLabels(project, services, opts.OnFatalWarning); err != nil {
		return nil, err
	}
	if err := validateSharedNamedVolumes(project, opts.OnFatalWarning); err != nil {
		return nil, err
	}
	if err := validateServiceNameservers(project, services); err != nil {
		return nil, err
	}
	networkNames := serviceNetworkNames(project, networkDefinitions)
	if err := preflightServiceOptions(project, services, networkNames, opts.OnWarning); err != nil {
		return nil, err
	}
	if err := c.validateNetworkOwnership(ctx, project, networkDefinitions, opts.OnFatalWarning); err != nil {
		return nil, err
	}
	existingServices, runningContainerIDs, orphanContainerIDs, retainedProjectNetworks, err := c.inspectUpState(ctx, project, services, opts.RemoveOrphans)
	if err != nil {
		return nil, err
	}
	existingAnonymousVolumes, err := anonymousVolumesByTarget(project.Name, project, existingServices)
	if err != nil {
		return nil, err
	}
	volumePlan, err := planNamedVolumes(ctx, project, services, &c.containerClient.Volumes, opts.OnWarning, existingAnonymousVolumes, opts.RenewAnonymousVolumes)
	if err != nil {
		return nil, err
	}
	selectedNetworkNames := uniqueServiceNetworkNames(networkNames, services)
	runtimeNetworkNames := mergeSortedUnique(selectedNetworkNames, retainedProjectNetworks)
	managedNetworkNames := composeNetworkRuntimeNames(networkDefinitions)
	knownNetworks, err := c.containerClient.Networks.ListSummaries(ctx)
	if err != nil {
		return nil, fmt.Errorf("list project networks for reconciliation: %w", err)
	}
	retainedNetworkNames := mergeSortedUnique(managedNetworkNames, runtimeNetworkNames)
	obsoleteNetworkNames := obsoleteProjectNetworkNames(project.Name, knownNetworks, retainedNetworkNames)
	imageIDs, err := c.resolveServiceImages(ctx, project, services, opts.Build, buildOutput)
	if err != nil {
		return nil, err
	}
	session := &UpSession{
		client:               c,
		project:              project,
		services:             append([]string(nil), services...),
		opts:                 opts,
		serviceNetworks:      networkNames,
		anonymousVolumes:     volumePlan.anonymousVolumes,
		imageIDs:             imageIDs,
		existingServices:     existingServices,
		runningContainerIDs:  runningContainerIDs,
		orphanContainerIDs:   orphanContainerIDs,
		selectedNetworkNames: selectedNetworkNames,
		runtimeNetworkNames:  runtimeNetworkNames,
		managedNetworkNames:  managedNetworkNames,
		obsoleteNetworkNames: obsoleteNetworkNames,
	}
	session.directPlans, err = session.planServices(ServiceStartOptions{})
	if err != nil {
		return nil, err
	}
	if c.registry != nil {
		if err := c.registry.Ensure(ctx); err != nil {
			return nil, fmt.Errorf("ensure service registry: %w", err)
		}
	}
	if err := prepareServiceBindMounts(project, services); err != nil {
		return nil, err
	}
	if _, err := c.ensureNetworks(ctx, project, networkDefinitions, opts.OnFatalWarning); err != nil {
		return nil, err
	}
	if err := volumePlan.apply(ctx, &c.containerClient.Volumes); err != nil {
		return nil, err
	}
	return session, nil
}

// StartServices starts the prepared services in dependency order. A DNS server
// is selected from each service's attached runtime networks; this is
// deliberately done here because Compose owns service/network membership.
func (p *UpSession) StartServices(ctx context.Context, startOpts ServiceStartOptions) (err error) {
	if p == nil || p.client == nil || p.client.containerClient == nil || p.project == nil {
		return ErrUpSessionInvalid
	}
	plans := p.directPlans
	if len(startOpts.DNSByNetwork) > 0 {
		plans, err = p.planServices(startOpts)
		if err != nil {
			return err
		}
	}
	if err := p.removeOrphans(ctx); err != nil {
		return err
	}

	var logSession *serviceLogSession
	if p.opts.Attach {
		logSession = newServiceLogSession(p.client, ctx, p.opts.Output, len(p.services))
		p.logSession = logSession
		defer func() {
			if err != nil {
				p.StopLogs()
			}
		}()
	}

	for _, plan := range plans {
		switch plan.action {
		case serviceStart:
			if _, err := p.client.containerClient.Container.Start(ctx, plan.existingID); err != nil && !isAlreadyRunningOrExisting(err) {
				return fmt.Errorf("start service %q: %w", plan.serviceName, err)
			}
		case serviceCreate, serviceRecreate:
			if plan.action == serviceRecreate {
				if _, running := p.runningContainerIDs[plan.existingID]; running {
					if _, err := p.client.containerClient.Container.Stop(ctx, container.StopOptions{IDs: []string{plan.existingID}}); err != nil && !isNotFoundLikeError(err) {
						return fmt.Errorf("stop service %q for recreation: %w", plan.serviceName, err)
					}
				}
				if _, err := p.client.containerClient.Container.Delete(ctx, container.DeleteOptions{IDs: []string{plan.existingID}}); err != nil && !isNotFoundLikeError(err) {
					return fmt.Errorf("delete service %q for recreation: %w", plan.serviceName, err)
				}
			}
			if _, err := p.client.containerClient.Container.Run(ctx, plan.image, plan.options); err != nil {
				return fmt.Errorf("start service %q: %w", plan.serviceName, err)
			}
		}
		if err := p.client.registerServiceRuntime(ctx, p.project, plan.service, plan.serviceName, plan.name); err != nil {
			warnRegistryFailure(p.opts.OnWarning, err)
		}
		if logSession != nil {
			logSession.Start(p.project.Name, plan.serviceName)
		}
	}

	return nil
}

func (p *UpSession) planServices(startOpts ServiceStartOptions) ([]servicePlan, error) {
	if err := validateDNSByNetwork(startOpts.DNSByNetwork, p.selectedNetworkNames); err != nil {
		return nil, err
	}
	plans := make([]servicePlan, 0, len(p.services))
	for _, serviceName := range p.services {
		service, err := p.project.GetService(serviceName)
		if err != nil {
			return nil, err
		}
		image, err := serviceImage(p.project.Name, serviceName, service)
		if err != nil {
			return nil, err
		}
		name := containerName(p.project.Name, serviceName)
		options, err := toCreateOptions(p.project, service, p.project.Name, serviceName, name, p.serviceNetworks[serviceName], p.anonymousVolumes[serviceName], nil)
		if err != nil {
			return nil, err
		}
		if len(startOpts.DNSByNetwork) > 0 {
			dns, err := dnsForService(p.serviceNetworks[serviceName], startOpts.DNSByNetwork)
			if err != nil {
				return nil, fmt.Errorf("service %q: %w", serviceName, err)
			}
			options.Nameservers = []string{dns.String()}
		}
		if imageID := p.imageIDs[serviceName]; imageID != "" {
			options.Labels[accImageIDLabel] = imageID
		}
		configHash, err := serviceConfigHash(image, options)
		if err != nil {
			return nil, fmt.Errorf("hash service %q configuration: %w", serviceName, err)
		}
		options.Labels[accConfigHashLabel] = configHash
		if err := container.ValidateRunOptions(image, options); err != nil {
			return nil, fmt.Errorf("service %q runtime options: %w", serviceName, err)
		}

		plan := servicePlan{serviceName: serviceName, service: service, image: image, name: name, options: options, action: serviceCreate}
		if existing, exists := p.existingServices[serviceName]; exists {
			plan.existingID = existing.summary.ID
			existingImageID := existing.summary.Labels[accImageIDLabel]
			if existingImageID == "" && service.Build == nil {
				// A first run can pull the image after the local-image check.
				existingImageID = existing.details.ImageDigest
			}
			imageChanged := p.imageIDs[serviceName] != "" && existingImageID != p.imageIDs[serviceName]
			forceBuiltImageRecreation := p.opts.Build && service.Build != nil && p.imageIDs[serviceName] == ""
			if existing.summary.Labels[accConfigHashLabel] != configHash || imageChanged || forceBuiltImageRecreation {
				plan.action = serviceRecreate
			} else if _, running := p.runningContainerIDs[existing.summary.ID]; running {
				plan.action = serviceReuse
			} else {
				plan.action = serviceStart
			}
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

func preflightServiceOptions(project *types.Project, services []string, networks map[string][]string, onWarning func(string)) error {
	for _, serviceName := range services {
		service, err := project.GetService(serviceName)
		if err != nil {
			return err
		}
		image, err := serviceImage(project.Name, serviceName, service)
		if err != nil {
			return err
		}
		anonymous := make([]string, 0)
		for index, volume := range service.Volumes {
			if volume.Type == types.VolumeTypeVolume && volume.Source == "" {
				anonymous = append(anonymous, fmt.Sprintf("acc-%s-anon-preflight-%d", project.Name, index))
			}
		}
		options, err := toCreateOptions(project, service, project.Name, serviceName, containerName(project.Name, serviceName), networks[serviceName], anonymous, onWarning)
		if err != nil {
			return fmt.Errorf("service %q runtime options: %w", serviceName, err)
		}
		if err := container.ValidateRunOptions(image, options); err != nil {
			return fmt.Errorf("service %q runtime options: %w", serviceName, err)
		}
	}
	return nil
}

func serviceImage(projectName, serviceName string, service types.ServiceConfig) (string, error) {
	if service.Image != "" {
		return service.Image, nil
	}
	if service.Build != nil {
		return containerName(projectName, serviceName), nil
	}
	return "", fmt.Errorf("service %q has neither image nor build", serviceName)
}

// WaitForLogs keeps an attached up running after service and resource
// reconciliation has finished. It returns immediately for detached sessions.
func (p *UpSession) WaitForLogs() error {
	if p == nil || p.client == nil || p.project == nil {
		return ErrUpSessionInvalid
	}
	if p.logSession == nil {
		return nil
	}
	logs := p.logSession
	p.logSession = nil
	return logs.Wait(p.opts.Detach)
}

// StopLogs stops any attached log streams after a later reconciliation error.
func (p *UpSession) StopLogs() {
	if p == nil || p.logSession == nil {
		return
	}
	logs := p.logSession
	p.logSession = nil
	logs.cancel()
	logs.wg.Wait()
}

// ServiceNames returns the services selected for reconciliation in dependency order.
func (p *UpSession) ServiceNames() []string {
	if p == nil {
		return nil
	}
	return append([]string(nil), p.services...)
}

func uniqueServiceNetworkNames(serviceNetworks map[string][]string, services []string) []string {
	set := make(map[string]struct{})
	for _, serviceName := range services {
		for _, networkName := range serviceNetworks[serviceName] {
			set[networkName] = struct{}{}
		}
	}
	names := make([]string, 0, len(set))
	for networkName := range set {
		names = append(names, networkName)
	}
	sort.Strings(names)
	return names
}

func composeNetworkRuntimeNames(networks map[string]composeNetwork) []string {
	names := make([]string, 0, len(networks))
	for _, network := range networks {
		if !network.external {
			names = append(names, network.runtimeName)
		}
	}
	return sortedUniqueStrings(names)
}

func mergeSortedUnique(left, right []string) []string {
	return sortedUniqueStrings(append(append([]string(nil), left...), right...))
}

func sortedUniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if value == "" || (len(result) > 0 && result[len(result)-1] == value) {
			continue
		}
		result = append(result, value)
	}
	return result
}

func serviceConfigHash(image string, opts container.CreateOptions) (string, error) {
	copyOpts := opts
	copyOpts.Labels = make(map[string]string, len(opts.Labels))
	for key, value := range opts.Labels {
		if key != accConfigHashLabel && key != accImageIDLabel {
			copyOpts.Labels[key] = value
		}
	}
	nameservers, err := container.NormalizeNameservers(copyOpts.Nameservers)
	if err != nil {
		return "", err
	}
	copyOpts.Nameservers = nameservers
	payload, err := json.Marshal(struct {
		Version int
		Image   string
		Options container.CreateOptions
	}{
		Version: 1,
		Image:   image,
		Options: copyOpts,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func (p *UpSession) removeOrphans(ctx context.Context) error {
	if !p.opts.RemoveOrphans || len(p.orphanContainerIDs) == 0 {
		return nil
	}
	running := make([]string, 0, len(p.orphanContainerIDs))
	for _, id := range p.orphanContainerIDs {
		if _, found := p.runningContainerIDs[id]; found {
			running = append(running, id)
		}
	}
	if len(running) > 0 {
		if _, err := p.client.containerClient.Container.Stop(ctx, container.StopOptions{IDs: running}); err != nil && !isNotFoundLikeError(err) {
			return fmt.Errorf("stop orphan containers: %w", err)
		}
	}
	if _, err := p.client.containerClient.Container.Delete(ctx, container.DeleteOptions{IDs: p.orphanContainerIDs}); err != nil && !isNotFoundLikeError(err) {
		return fmt.Errorf("delete orphan containers: %w", err)
	}
	if p.client.registry != nil {
		if err := p.client.registry.Remove(ctx, p.orphanContainerIDs); err != nil {
			warnRegistryFailure(p.opts.OnWarning, fmt.Errorf("remove orphan service registry records: %w", err))
		}
	}
	return nil
}

// RemoveObsoleteNetworks removes ACC-owned project networks that are no
// longer declared or retained for an orphan after service reconciliation.
func (p *UpSession) RemoveObsoleteNetworks(ctx context.Context) error {
	if p == nil || p.client == nil || p.client.containerClient == nil || p.project == nil {
		return ErrUpSessionInvalid
	}
	if len(p.obsoleteNetworkNames) == 0 {
		return nil
	}
	networks, err := p.client.containerClient.Networks.ListSummaries(ctx)
	if err != nil {
		return err
	}
	retained := mergeSortedUnique(p.managedNetworkNames, p.runtimeNetworkNames)
	stillObsolete := obsoleteProjectNetworkNames(p.project.Name, networks, retained)
	stillObsoleteSet := make(map[string]struct{}, len(stillObsolete))
	for _, name := range stillObsolete {
		stillObsoleteSet[name] = struct{}{}
	}
	obsolete := make([]string, 0, len(p.obsoleteNetworkNames))
	for _, name := range p.obsoleteNetworkNames {
		if _, found := stillObsoleteSet[name]; found {
			obsolete = append(obsolete, name)
		}
	}
	if len(obsolete) == 0 {
		return nil
	}
	if _, err := p.client.containerClient.Networks.Delete(ctx, obsolete); err != nil && !isNotFoundLikeError(err) {
		return fmt.Errorf("remove obsolete project networks: %w", err)
	}
	return nil
}

func obsoleteProjectNetworkNames(projectName string, networks []container.NetworkSummary, retained []string) []string {
	keep := make(map[string]struct{}, len(retained))
	for _, name := range retained {
		keep[name] = struct{}{}
	}
	obsolete := make([]string, 0)
	for _, network := range networks {
		if network.Labels[accProjectLabel] != projectName || network.Labels[accNetworkLabel] == "" {
			continue
		}
		if _, found := keep[network.Name]; !found {
			obsolete = append(obsolete, network.Name)
		}
	}
	return sortedUniqueStrings(obsolete)
}

func validateDNSByNetwork(dnsByNetwork map[string]netip.Addr, networks []string) error {
	if len(dnsByNetwork) == 0 {
		return nil
	}
	for networkName, address := range dnsByNetwork {
		if strings.TrimSpace(networkName) == "" || !address.IsValid() || !address.IsGlobalUnicast() {
			return ErrInvalidDNSConfig
		}
	}
	for _, networkName := range networks {
		if _, found := dnsByNetwork[networkName]; !found {
			return fmt.Errorf("%w: no DNS server for network %q", ErrInvalidDNSConfig, networkName)
		}
	}
	return nil
}

func dnsForService(networks []string, dnsByNetwork map[string]netip.Addr) (netip.Addr, error) {
	for _, networkName := range networks {
		if address, found := dnsByNetwork[networkName]; found {
			return address, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("%w: service has no network with a DNS server", ErrInvalidDNSConfig)
}

func warnRegistryFailure(onWarning func(string), err error) {
	if onWarning != nil && err != nil {
		onWarning(err.Error())
	}
}

func validateServiceNameservers(project *types.Project, services []string) error {
	for _, serviceName := range services {
		service, err := project.GetService(serviceName)
		if err != nil {
			return err
		}
		if _, err := container.NormalizeNameservers([]string(service.DNS)); err != nil {
			return fmt.Errorf("service %q dns: %w", serviceName, err)
		}
	}
	return nil
}

func (c *ComposeClient) registerServiceRuntime(ctx context.Context, project *types.Project, service types.ServiceConfig, serviceName, containerName string) error {
	if c.registry == nil {
		return nil
	}
	details, err := c.containerClient.Container.InspectDetails(ctx, []string{containerName})
	if err != nil {
		return fmt.Errorf("inspect service %q for registry: %w", serviceName, err)
	}
	if len(details) != 1 || details[0].ID == "" {
		return fmt.Errorf("inspect service %q for registry: expected one identified container", serviceName)
	}
	runtime := ServiceRuntime{
		ProjectName: project.Name,
		ServiceName: serviceName,
		ContainerID: details[0].ID,
		Nameservers: append([]string(nil), service.DNS...),
		Networks:    make([]ServiceNetworkRuntime, 0, len(details[0].Networks)),
	}
	for _, attachment := range details[0].Networks {
		runtime.Networks = append(runtime.Networks, ServiceNetworkRuntime{
			Name:      attachment.Name,
			Addresses: append([]netip.Addr(nil), attachment.Addresses...),
			Aliases:   serviceNetworkAliases(project, service, attachment.Name),
		})
	}
	if err := c.registry.Upsert(ctx, runtime); err != nil {
		return fmt.Errorf("update service registry for service %q: %w", serviceName, err)
	}
	return nil
}

func serviceNetworkAliases(project *types.Project, service types.ServiceConfig, runtimeNetwork string) []string {
	for logicalName, config := range service.Networks {
		if config == nil || project.Networks[logicalName].Name != runtimeNetwork {
			continue
		}
		return append([]string(nil), config.Aliases...)
	}
	return nil
}

func (c *ComposeClient) inspectUpState(ctx context.Context, project *types.Project, services []string, removeOrphans bool) (map[string]existingService, map[string]struct{}, []string, []string, error) {
	allContainers, err := c.containerClient.Container.ListSummaries(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if len(allContainers) == 0 {
		return map[string]existingService{}, map[string]struct{}{}, nil, nil, nil
	}
	runningContainers, err := c.containerClient.Container.ListSummaries(ctx, container.ListOptions{})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	runningIDs := make(map[string]struct{}, len(runningContainers))
	for _, item := range runningContainers {
		runningIDs[item.ID] = struct{}{}
	}

	selected := make(map[string]struct{}, len(services))
	for _, serviceName := range services {
		selected[serviceName] = struct{}{}
	}
	active := make(map[string]struct{}, len(project.Services))
	for serviceName := range project.Services {
		active[serviceName] = struct{}{}
	}

	byID := make(map[string]container.ContainerSummary, len(allContainers))
	orphanIDs := make([]string, 0)
	inspectIDs := make([]string, 0)
	retainedContainerIDs := make([]string, 0)
	for _, item := range allContainers {
		byID[item.ID] = item
		if item.Labels[accProjectLabel] != project.Name {
			continue
		}
		serviceName := strings.TrimSpace(item.Labels[accServiceLabel])
		if serviceName == "" {
			continue
		}
		if _, exists := active[serviceName]; !exists {
			orphanIDs = append(orphanIDs, item.ID)
			if !removeOrphans {
				inspectIDs = append(inspectIDs, item.ID)
				retainedContainerIDs = append(retainedContainerIDs, item.ID)
			}
			continue
		}
		if _, wanted := selected[serviceName]; wanted {
			if item.ID != containerName(project.Name, serviceName) {
				return nil, nil, nil, nil, fmt.Errorf("service %q has ACC-owned container %q, expected %q", serviceName, item.ID, containerName(project.Name, serviceName))
			}
			continue
		}
		inspectIDs = append(inspectIDs, item.ID)
		retainedContainerIDs = append(retainedContainerIDs, item.ID)
	}

	existing := make(map[string]existingService, len(services))
	for _, serviceName := range services {
		name := containerName(project.Name, serviceName)
		item, found := byID[name]
		if !found {
			continue
		}
		if item.Labels[accProjectLabel] != project.Name || item.Labels[accServiceLabel] != serviceName {
			return nil, nil, nil, nil, fmt.Errorf("container name %q is already in use by a container not owned by ACC service %q", name, serviceName)
		}
		existing[serviceName] = existingService{summary: item}
		inspectIDs = append(inspectIDs, item.ID)
	}

	sort.Strings(orphanIDs)
	inspectIDs = sortedUniqueStrings(inspectIDs)
	detailsByID := make(map[string]container.ContainerDetails, len(inspectIDs))
	if len(inspectIDs) > 0 {
		details, err := c.containerClient.Container.InspectDetails(ctx, inspectIDs)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		for _, item := range details {
			detailsByID[item.ID] = item
		}
		for _, id := range inspectIDs {
			if _, found := detailsByID[id]; !found {
				return nil, nil, nil, nil, fmt.Errorf("inspect container %q: expected one identified container", id)
			}
		}
	}
	for serviceName, item := range existing {
		item.details = detailsByID[item.summary.ID]
		existing[serviceName] = item
	}

	retainedNetworks := make([]string, 0)
	for _, id := range retainedContainerIDs {
		for _, network := range detailsByID[id].Networks {
			retainedNetworks = append(retainedNetworks, network.Name)
		}
	}
	return existing, runningIDs, orphanIDs, sortedUniqueStrings(retainedNetworks), nil
}

func anonymousVolumesByTarget(projectName string, project *types.Project, services map[string]existingService) (map[string]map[string]string, error) {
	result := make(map[string]map[string]string)
	for serviceName, existing := range services {
		service, err := project.GetService(serviceName)
		if err != nil {
			return nil, err
		}
		desiredTargets := make([]string, 0)
		for _, volume := range service.Volumes {
			if volume.Type == types.VolumeTypeVolume && volume.Source == "" {
				desiredTargets = append(desiredTargets, volume.Target)
			}
		}
		if len(desiredTargets) == 0 {
			continue
		}
		byTarget := make(map[string]string)
		unmapped := make([]string, 0)
		for _, mount := range existing.details.Mounts {
			if mount.Type != container.MountTypeVolume || !strings.HasPrefix(mount.Source, "acc-"+projectName+"-anon-") {
				continue
			}
			if mount.Target == "" {
				unmapped = append(unmapped, mount.Source)
				continue
			}
			byTarget[mount.Target] = mount.Source
		}
		if len(desiredTargets) == 1 && len(byTarget) == 0 && len(unmapped) == 1 {
			byTarget[desiredTargets[0]] = unmapped[0]
		} else if len(unmapped) > 0 {
			return nil, fmt.Errorf("service %q has anonymous volumes whose mount targets could not be inspected; refusing to recreate it without a safe volume mapping", serviceName)
		}
		if len(byTarget) > 0 {
			result[serviceName] = byTarget
		}
	}
	return result, nil
}

// resolveServiceImages completes the build phase before any service starts.
func (c *ComposeClient) resolveServiceImages(ctx context.Context, project *types.Project, services []string, forceBuild bool, output io.Writer) (map[string]string, error) {
	imageIDs := make(map[string]string)
	if forceBuild {
		for _, serviceName := range services {
			svc, err := project.GetService(serviceName)
			if err != nil {
				return nil, err
			}
			if svc.Build == nil {
				if err := c.recordLocalImageID(ctx, serviceName, svc.Image, imageIDs); err != nil {
					return nil, err
				}
				continue
			}
			imageID, err := c.buildServiceImage(ctx, project, serviceName, svc, output)
			if err != nil {
				return nil, err
			}
			if svc.Build != nil {
				image := svc.Image
				if image == "" {
					image = containerName(project.Name, serviceName)
				}
				summary, exists, err := c.containerClient.Images.InspectSummary(ctx, image)
				if err != nil {
					return nil, err
				}
				if exists && summary.Digest != "" {
					imageID = summary.Digest
				}
			}
			if imageID != "" {
				imageIDs[serviceName] = imageID
			}
		}
		return imageIDs, nil
	}

	for _, serviceName := range services {
		svc, err := project.GetService(serviceName)
		if err != nil {
			return nil, err
		}
		if svc.Build == nil {
			if err := c.recordLocalImageID(ctx, serviceName, svc.Image, imageIDs); err != nil {
				return nil, err
			}
			continue
		}

		image := svc.Image
		if image == "" {
			image = containerName(project.Name, serviceName)
		}
		summary, exists, err := c.containerClient.Images.InspectSummary(ctx, image)
		if err != nil {
			return nil, err
		}
		if exists && summary.Digest != "" {
			imageIDs[serviceName] = summary.Digest
		}
		if !exists {
			imageID, err := c.buildServiceImage(ctx, project, serviceName, svc, output)
			if err != nil {
				return nil, err
			}
			summary, exists, err := c.containerClient.Images.InspectSummary(ctx, image)
			if err != nil {
				return nil, err
			}
			if exists && summary.Digest != "" {
				imageID = summary.Digest
			}
			if imageID != "" {
				imageIDs[serviceName] = imageID
			}
		}
	}
	return imageIDs, nil
}

func (c *ComposeClient) recordLocalImageID(ctx context.Context, serviceName, image string, imageIDs map[string]string) error {
	if image == "" {
		return nil
	}
	summary, exists, err := c.containerClient.Images.InspectSummary(ctx, image)
	if err != nil {
		return fmt.Errorf("inspect local image for service %q: %w", serviceName, err)
	}
	if exists && summary.Digest != "" {
		imageIDs[serviceName] = summary.Digest
	}
	return nil
}

type serviceLogSession struct {
	client *ComposeClient
	ctx    context.Context
	cancel context.CancelFunc
	writer io.Writer
	errCh  chan error
	wg     sync.WaitGroup
}

func newServiceLogSession(client *ComposeClient, ctx context.Context, output io.Writer, serviceCount int) *serviceLogSession {
	if output == nil {
		output = io.Discard
	}
	streamCtx, cancel := context.WithCancel(ctx)
	return &serviceLogSession{
		client: client,
		ctx:    streamCtx,
		cancel: cancel,
		writer: &synchronizedWriter{w: output},
		errCh:  make(chan error, serviceCount),
	}
}

// Start begins following one service as soon as it has been started.
func (s *serviceLogSession) Start(projectName, serviceName string) {
	containerID := containerName(projectName, serviceName)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		prefixed := newPrefixedWriter(s.writer, "["+serviceName+"] ")
		if err := s.followServiceLogs(containerID, prefixed); err != nil && !errors.Is(err, context.Canceled) {
			s.errCh <- err
		}
	}()
}

// Wait streams logs until context cancellation, detach, or a stream error.
func (s *serviceLogSession) Wait(detach <-chan struct{}) error {
	defer s.cancel()

	select {
	case <-s.ctx.Done():
	case err := <-s.errCh:
		s.cancel()
		s.wg.Wait()
		return err
	case <-detach:
		s.cancel()
	}

	s.wg.Wait()
	return nil
}

func (s *serviceLogSession) followServiceLogs(containerID string, output io.Writer) error {
	const retryDelay = 100 * time.Millisecond

	for {
		_, err := s.client.containerClient.Container.Logs(s.ctx, container.LogsOptions{
			Follow: true,
			IDs:    []string{containerID},
		}, output)
		if s.ctx.Err() != nil {
			return s.ctx.Err()
		}
		if err != nil && !isNotFoundLikeError(err) {
			return err
		}

		timer := time.NewTimer(retryDelay)
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return s.ctx.Err()
		case <-timer.C:
		}
	}
}

type synchronizedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *synchronizedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

type prefixedWriter struct {
	target io.Writer
	prefix string
	buf    strings.Builder
}

func newPrefixedWriter(target io.Writer, prefix string) *prefixedWriter {
	return &prefixedWriter{target: target, prefix: prefix}
}

func (w *prefixedWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		w.buf.WriteByte(b)
		if b == '\n' {
			line := w.buf.String()
			w.buf.Reset()
			if _, err := io.WriteString(w.target, w.prefix+line); err != nil {
				return 0, err
			}
		}
	}
	return len(p), nil
}

// toCreateOptions converts compose service run-time settings into container create options.
func toCreateOptions(project *types.Project, service types.ServiceConfig, projectName, serviceName, name string, networkNames, anonymousVolumes []string, onWarning func(string)) (container.CreateOptions, error) {
	mounts, err := mountsForService(project, service, anonymousVolumes)
	if err != nil {
		return container.CreateOptions{}, err
	}

	createOpts := container.CreateOptions{
		Name:        name,
		Environment: serviceEnvironmentToList(service.Environment),
		Labels: map[string]string{
			accProjectLabel: projectName,
			accServiceLabel: serviceName,
		},
		Nameservers: []string(service.DNS),
		Networks:    networkNames,
		Mounts:      mounts,
		Arguments:   shellCommandToArgs(service.Command),
	}

	// compose-go validates that service-level and deploy limits agree when
	// both are set. Map either syntax to the same effective runtime options,
	// so reconciliation depends on the allocation, not the spelling used.
	cpuLimit := service.CPUS
	memoryLimit := service.MemLimit
	if service.Deploy != nil && service.Deploy.Resources.Limits != nil {
		limits := service.Deploy.Resources.Limits
		if limits.NanoCPUs != 0 {
			cpuLimit = limits.NanoCPUs.Value()
		}
		if limits.MemoryBytes != 0 {
			memoryLimit = limits.MemoryBytes
		}
	}

	if cpuLimit > 0 {
		cpus := math.Ceil(float64(cpuLimit))
		if math.IsNaN(cpus) || math.IsInf(cpus, 0) || cpus > float64(^uint(0)-1) {
			return container.CreateOptions{}, fmt.Errorf("%w: invalid CPU allocation for service %q", ErrUnsupportedFeature, serviceName)
		}
		createOpts.CPUs = uint(cpus)
		if onWarning != nil && cpus != float64(cpuLimit) {
			unit := "CPUs"
			if createOpts.CPUs == 1 {
				unit = "CPU"
			}
			onWarning(fmt.Sprintf("service %q requests %g CPUs; Apple Container supports whole CPU counts, so ACC will allocate %d %s", serviceName, cpuLimit, createOpts.CPUs, unit))
		}
	}
	if memoryLimit > 0 {
		createOpts.Memory = formatMemory(uint64(memoryLimit))
	}

	mappings := make([]container.PortMapping, 0, len(service.Ports))
	for _, port := range service.Ports {
		if port.Target == 0 {
			continue
		}
		if strings.TrimSpace(port.Published) == "" {
			return container.CreateOptions{}, fmt.Errorf("%w: unpublished or ephemeral ports are not supported for service %q", ErrUnsupportedFeature, service.Name)
		}

		val, err := strconv.ParseUint(port.Published, 10, 16)
		if err != nil {
			return container.CreateOptions{}, fmt.Errorf("%w: published port %q for service %q", ErrUnsupportedFeature, port.Published, service.Name)
		}
		hostPort := uint16(val)

		mapping := container.PortMapping{
			HostIP:        port.HostIP,
			HostPort:      hostPort,
			ContainerPort: uint16(port.Target),
		}

		if strings.EqualFold(port.Protocol, "udp") {
			mapping.Protocol = container.UDP
		} else {
			mapping.Protocol = container.TCP
		}

		mappings = append(mappings, mapping)
	}

	if len(mappings) > 0 {
		createOpts.Publish = mappings
	}

	return createOpts, nil
}

// serviceEnvironmentToList converts compose environment entries into container
// CLI --env flag values.
func serviceEnvironmentToList(env types.MappingWithEquals) []string {
	if len(env) == 0 {
		return nil
	}

	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	entries := make([]string, 0, len(keys))
	for _, key := range keys {
		val := env[key]
		if val == nil {
			entries = append(entries, key)
			continue
		}
		entries = append(entries, key+"="+*val)
	}

	return entries
}

// formatMemory converts Compose's byte-based memory limit to the container
// CLI's MiByte-based memory format.
func formatMemory(bytes uint64) string {
	const mebibyte = 1024 * 1024
	mebibytes := (bytes + mebibyte - 1) / mebibyte
	return strconv.FormatUint(mebibytes, 10) + "M"
}

// shellCommandToArgs drops blank entries and returns command args for container run.
func shellCommandToArgs(cmd types.ShellCommand) []string {
	if len(cmd) == 0 {
		return nil
	}
	args := make([]string, 0, len(cmd))
	for _, arg := range cmd {
		if strings.TrimSpace(arg) == "" {
			continue
		}
		args = append(args, arg)
	}
	return args
}

// isAlreadyRunningOrExisting normalizes runtime errors for idempotent up operations.
func isAlreadyRunningOrExisting(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "already exists") || strings.Contains(msg, "is already running")
}
