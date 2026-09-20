package compose

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	client                    *ComposeClient
	project                   *types.Project
	services                  []string
	opts                      UpOptions
	serviceNetworks           map[string][]string
	anonymousVolumes          map[string][]string
	existingAnonymousServices map[string]bool
	runtimeNetworkNames       []string
}

// ProjectName returns the resolved project name for this startup session.
func (p *UpSession) ProjectName() string {
	if p == nil || p.project == nil {
		return ""
	}
	return p.project.Name
}

// NetworkNames returns the sorted runtime networks used by selected services.
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
	return session.StartServices(ctx, ServiceStartOptions{})
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
	if c.registry != nil {
		if err := c.registry.Ensure(ctx); err != nil {
			return nil, fmt.Errorf("ensure service registry: %w", err)
		}
	}
	if err := c.resolveServiceImages(ctx, project, services, opts.Build, buildOutput); err != nil {
		return nil, err
	}
	if err := prepareServiceBindMounts(project, services); err != nil {
		return nil, err
	}
	existingAnonymousServices, err := c.existingAnonymousServices(ctx, project, services)
	if err != nil {
		return nil, err
	}
	anonymousVolumes, err := prepareNamedVolumes(ctx, project, services, &c.containerClient.Volumes, opts.OnWarning, existingAnonymousServices)
	if err != nil {
		return nil, err
	}
	networkNames, err := c.ensureNetworks(ctx, project, networkDefinitions, opts.OnFatalWarning)
	if err != nil {
		return nil, err
	}
	return &UpSession{
		client:                    c,
		project:                   project,
		services:                  append([]string(nil), services...),
		opts:                      opts,
		serviceNetworks:           networkNames,
		anonymousVolumes:          anonymousVolumes,
		existingAnonymousServices: existingAnonymousServices,
		runtimeNetworkNames:       uniqueServiceNetworkNames(networkNames, services),
	}, nil
}

// StartServices starts the prepared services in dependency order. A DNS server
// is selected from each service's attached runtime networks; this is
// deliberately done here because Compose owns service/network membership.
func (p *UpSession) StartServices(ctx context.Context, startOpts ServiceStartOptions) error {
	if p == nil || p.client == nil || p.client.containerClient == nil || p.project == nil {
		return ErrUpSessionInvalid
	}
	if err := validateDNSByNetwork(startOpts.DNSByNetwork, p.runtimeNetworkNames); err != nil {
		return err
	}

	var logSession *serviceLogSession
	if p.opts.Attach {
		logSession = newServiceLogSession(p.client, ctx, p.opts.Output, len(p.services))
	}

	for _, serviceName := range p.services {
		svc, err := p.project.GetService(serviceName)
		if err != nil {
			return err
		}

		image := svc.Image
		if image == "" {
			if svc.Build == nil {
				return fmt.Errorf("service %q has neither image nor build", serviceName)
			}
			image = containerName(p.project.Name, serviceName)
		}
		name := containerName(p.project.Name, serviceName)
		if p.existingAnonymousServices[serviceName] {
			if err := p.client.registerServiceRuntime(ctx, p.project, svc, serviceName, name); err != nil {
				warnRegistryFailure(p.opts.OnWarning, err)
			}
			if logSession != nil {
				logSession.Start(p.project.Name, serviceName)
			}
			continue
		}

		createOpts, err := toCreateOptions(p.project, svc, p.project.Name, serviceName, name, p.serviceNetworks[serviceName], p.anonymousVolumes[serviceName])
		if err != nil {
			return err
		}
		if len(startOpts.DNSByNetwork) > 0 {
			dns, err := dnsForService(p.serviceNetworks[serviceName], startOpts.DNSByNetwork)
			if err != nil {
				return fmt.Errorf("service %q: %w", serviceName, err)
			}
			createOpts.Nameservers = []string{dns.String()}
		}

		if _, err := p.client.containerClient.Container.Run(ctx, image, createOpts); err != nil {
			if !isAlreadyRunningOrExisting(err) {
				return err
			}
		}
		if err := p.client.registerServiceRuntime(ctx, p.project, svc, serviceName, name); err != nil {
			warnRegistryFailure(p.opts.OnWarning, err)
		}
		if logSession != nil {
			logSession.Start(p.project.Name, serviceName)
		}
	}

	if logSession != nil {
		return logSession.Wait(p.opts.Detach)
	}
	return nil
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

func (c *ComposeClient) existingAnonymousServices(ctx context.Context, project *types.Project, services []string) (map[string]bool, error) {
	candidates := make(map[string]struct{})
	for _, serviceName := range services {
		service, err := project.GetService(serviceName)
		if err != nil {
			return nil, err
		}
		for _, volume := range service.Volumes {
			if volume.Type == types.VolumeTypeVolume && volume.Source == "" {
				candidates[serviceName] = struct{}{}
				break
			}
		}
	}
	if len(candidates) == 0 {
		return map[string]bool{}, nil
	}

	containers, err := c.containerClient.Container.ListSummaries(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, err
	}
	existing := make(map[string]bool)
	for _, item := range containers {
		if item.Labels[accProjectLabel] != project.Name {
			continue
		}
		serviceName := item.Labels[accServiceLabel]
		if _, ok := candidates[serviceName]; ok {
			existing[serviceName] = true
		}
	}
	return existing, nil
}

// resolveServiceImages completes the build phase before any service starts.
func (c *ComposeClient) resolveServiceImages(ctx context.Context, project *types.Project, services []string, forceBuild bool, output io.Writer) error {
	if forceBuild {
		return c.buildProject(ctx, project, BuildOptions{Services: services, Output: output})
	}

	for _, serviceName := range services {
		svc, err := project.GetService(serviceName)
		if err != nil {
			return err
		}
		if svc.Build == nil {
			continue
		}

		image := svc.Image
		if image == "" {
			image = containerName(project.Name, serviceName)
		}
		exists, err := c.containerClient.Images.Exists(ctx, image)
		if err != nil {
			return err
		}
		if !exists {
			if err := c.buildService(ctx, project, serviceName, svc, output); err != nil {
				return err
			}
		}
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
func toCreateOptions(project *types.Project, service types.ServiceConfig, projectName, serviceName, name string, networkNames, anonymousVolumes []string) (container.CreateOptions, error) {
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

	if service.CPUS > 0 {
		createOpts.CPUs = uint(service.CPUS)
	}
	if service.MemLimit > 0 {
		createOpts.Memory = formatMemory(uint64(service.MemLimit))
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
