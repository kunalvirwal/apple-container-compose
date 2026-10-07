package compose

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/kunalvirwal/apple-container-compose/pkg/container"
)

// DownOptions controls compose down behavior.
type DownOptions struct {
	// Services selects services and their transitive dependents for teardown.
	// Empty means all services.
	Services []string
	// RemoveOrphans also removes project containers whose service is no longer
	// declared in the current Compose file.
	RemoveOrphans bool
	// Force forces container deletion when supported by the runtime.
	Force bool
	// Volumes removes eligible ACC-owned named and anonymous volumes. With
	// Services set, only volumes attached to containers removed by this invocation
	// are eligible, and volumes referenced by any remaining container are retained.
	// Without Services, detached project-owned named volumes are also eligible.
	Volumes bool
	// OnWarning receives non-fatal warnings. The SDK never renders warnings
	// itself; callers decide whether and how to present them.
	OnWarning func(string)
}

// DownSession is a prepared Compose teardown. It separates service removal
// from project-resource cleanup so applications can manage their own
// infrastructure between those phases.
type DownSession struct {
	client              *ComposeClient
	projectName         string
	serviceIDs          []string
	attachedVolumeNames map[string]struct{}
	retainedVolumeNames map[string]struct{}
	opts                DownOptions
}

// ProjectName returns the resolved Compose project name for this teardown.
func (p *DownSession) ProjectName() string {
	if p == nil {
		return ""
	}
	return p.projectName
}

// Down stops and removes labeled project containers in reverse dependency order.
func (c *ComposeClient) Down(ctx context.Context, path string, parseOpts ParseOptions, opts DownOptions) error {
	session, err := c.PrepareDown(ctx, path, parseOpts, opts)
	if err != nil {
		return err
	}
	if err := session.RemoveServices(ctx); err != nil {
		return err
	}
	return session.RemoveResources(ctx)
}

// PrepareDown loads the Compose project and identifies the service containers
// and volumes that a teardown will affect. It does not remove any resources.
func (c *ComposeClient) PrepareDown(ctx context.Context, path string, parseOpts ParseOptions, opts DownOptions) (*DownSession, error) {
	if c.containerClient == nil {
		return nil, ErrContainerClientNil
	}

	project, err := c.loadProject(ctx, path, parseOpts)
	if err != nil {
		return nil, err
	}

	services, err := generateDownServiceOrder(project, opts.Services)
	if err != nil {
		return nil, err
	}

	containers, err := c.containerClient.Container.ListSummaries(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, err
	}

	serviceIDs := downContainerIDs(project, services, containers, opts.RemoveOrphans)
	attachedVolumeNames := make(map[string]struct{})
	retainedVolumeNames := make(map[string]struct{})
	for _, volume := range project.Volumes {
		if volume.External {
			retainedVolumeNames[volume.Name] = struct{}{}
		}
	}
	if opts.Volumes && len(serviceIDs) > 0 {
		details, err := c.containerClient.Container.InspectDetails(ctx, serviceIDs)
		if err != nil {
			if !isNotFoundLikeError(err) {
				return nil, err
			}
		} else {
			for _, item := range details {
				for _, name := range item.VolumeNames {
					attachedVolumeNames[name] = struct{}{}
				}
			}
		}
	}
	if opts.Volumes && len(opts.Services) == 0 && !opts.RemoveOrphans {
		retainedOrphanIDs := orphanContainerIDs(project, containers)
		if len(retainedOrphanIDs) > 0 {
			details, err := c.containerClient.Container.InspectDetails(ctx, retainedOrphanIDs)
			if err != nil {
				if !isNotFoundLikeError(err) {
					return nil, err
				}
			} else {
				for _, item := range details {
					for _, name := range item.VolumeNames {
						retainedVolumeNames[name] = struct{}{}
					}
				}
			}
		}
	}
	return &DownSession{
		client:              c,
		projectName:         project.Name,
		serviceIDs:          append([]string(nil), serviceIDs...),
		attachedVolumeNames: attachedVolumeNames,
		retainedVolumeNames: retainedVolumeNames,
		opts:                opts,
	}, nil
}

// RemoveServices stops and deletes the selected Compose service containers.
func (p *DownSession) RemoveServices(ctx context.Context) error {
	if p == nil || p.client == nil || p.client.containerClient == nil {
		return ErrDownSessionInvalid
	}
	if len(p.serviceIDs) > 0 {
		if _, err := p.client.containerClient.Container.Stop(ctx, container.StopOptions{IDs: p.serviceIDs}); err != nil {
			if !isNotFoundLikeError(err) {
				return err
			}
		}

		if _, err := p.client.containerClient.Container.Delete(ctx, container.DeleteOptions{IDs: p.serviceIDs, Force: p.opts.Force}); err != nil {
			if !isNotFoundLikeError(err) {
				return err
			}
		}
		if p.client.registry != nil {
			if err := p.client.registry.Remove(ctx, p.serviceIDs); err != nil {
				warnRegistryFailure(p.opts.OnWarning, fmt.Errorf("remove service registry records: %w", err))
			}
		}
	}
	return nil
}

// RemoveResources removes eligible project volumes and networks after service
// containers and any application-managed infrastructure are gone.
func (p *DownSession) RemoveResources(ctx context.Context) error {
	if err := p.RemoveVolumes(ctx); err != nil {
		return err
	}
	return p.RemoveNetworks(ctx)
}

// RemoveVolumes removes eligible project volumes after the selected service
// containers are gone. For selected-service teardown, only attached volumes
// without references from any remaining container are eligible.
func (p *DownSession) RemoveVolumes(ctx context.Context) error {
	if p == nil || p.client == nil || p.client.containerClient == nil {
		return ErrDownSessionInvalid
	}
	if p.opts.Volumes {
		attachedOnly := len(p.opts.Services) > 0
		if attachedOnly {
			if len(p.attachedVolumeNames) == 0 {
				return nil
			}
			if err := p.retainReferencedVolumes(ctx); err != nil {
				return err
			}
		}
		if err := p.client.removeProjectVolumes(ctx, p.projectName, p.attachedVolumeNames, p.retainedVolumeNames, attachedOnly); err != nil {
			return err
		}
	}
	return nil
}

// retainReferencedVolumes checks all remaining containers, including stopped,
// foreign, and infrastructure containers. Incomplete inspection fails closed:
// missing container records are not evidence that a volume is unused.
func (p *DownSession) retainReferencedVolumes(ctx context.Context) error {
	containers, err := p.client.containerClient.Container.ListSummaries(ctx, container.ListOptions{All: true})
	if err != nil {
		return fmt.Errorf("list remaining containers for volume cleanup: %w", err)
	}
	if len(containers) == 0 {
		return nil
	}
	ids := make([]string, 0, len(containers))
	for _, item := range containers {
		ids = append(ids, item.ID)
	}
	sort.Strings(ids)
	details, err := p.client.containerClient.Container.InspectDetails(ctx, ids)
	if err != nil {
		return fmt.Errorf("inspect remaining containers for volume cleanup: %w", err)
	}
	inspected := make(map[string]struct{}, len(details))
	for _, item := range details {
		inspected[item.ID] = struct{}{}
		for _, name := range item.VolumeNames {
			p.retainedVolumeNames[name] = struct{}{}
		}
	}
	for _, id := range ids {
		if _, found := inspected[id]; !found {
			return fmt.Errorf("inspect remaining container %q for volume cleanup: missing container metadata", id)
		}
	}
	return nil
}

// RemoveNetworks removes eligible project networks after service containers
// and application-managed infrastructure are gone.
func (p *DownSession) RemoveNetworks(ctx context.Context) error {
	if p == nil || p.client == nil || p.client.containerClient == nil {
		return ErrDownSessionInvalid
	}
	if len(p.opts.Services) == 0 {
		containers, err := p.client.containerClient.Container.ListSummaries(ctx, container.ListOptions{All: true})
		if err != nil {
			return err
		}
		if !hasRemainingProjectContainers(p.projectName, containers) {
			if err := p.client.removeProjectNetworks(ctx, p.projectName); err != nil {
				return err
			}
		}
	}
	return nil
}

// removeProjectNetworks removes only networks demonstrably owned by this ACC
// project. Unlabelled and foreign networks are intentionally left untouched.
func (c *ComposeClient) removeProjectNetworks(ctx context.Context, projectName string) error {
	networks, err := c.containerClient.Networks.ListSummaries(ctx)
	if err != nil {
		return err
	}
	names := make([]string, 0)
	for _, network := range networks {
		if network.Labels[accProjectLabel] == projectName && network.Labels[accNetworkLabel] != "" {
			names = append(names, network.Name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil
	}
	if _, err := c.containerClient.Networks.Delete(ctx, names); err != nil && !isNotFoundLikeError(err) {
		return err
	}
	return nil
}

func hasRemainingProjectContainers(projectName string, containers []container.ContainerSummary) bool {
	for _, item := range containers {
		if item.Labels[accProjectLabel] == projectName {
			return true
		}
	}
	return false
}

// removeProjectVolumes removes all project-managed named volumes that are not
// mounted by retained orphan containers, plus explicitly-created anonymous
// volumes mounted by containers removed in this Down invocation. Detached
// anonymous volumes are retained: without their former container there is no
// safe association with this teardown. With attachedOnly, named volumes must
// also have been attached to a container removed by this invocation.
func (c *ComposeClient) removeProjectVolumes(ctx context.Context, projectName string, attachedVolumeNames, retainedVolumeNames map[string]struct{}, attachedOnly bool) error {
	volumes, err := c.containerClient.Volumes.ListSummaries(ctx)
	if err != nil {
		return err
	}

	names := make([]string, 0)
	for _, volume := range volumes {
		if volume.Labels[accProjectLabel] != projectName || volume.Labels[accVolumeLabel] == "" {
			continue
		}
		if _, retained := retainedVolumeNames[volume.Name]; retained {
			continue
		}
		if attachedOnly || isAnonymousVolume(projectName, volume) {
			if _, attached := attachedVolumeNames[volume.Name]; !attached {
				continue
			}
		}
		names = append(names, volume.Name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil
	}
	if _, err := c.containerClient.Volumes.Delete(ctx, container.VolumeDeleteOptions{Names: names}); err != nil && !isNotFoundLikeError(err) {
		return err
	}
	return nil
}

func isAnonymousVolume(projectName string, volume container.VolumeSummary) bool {
	prefix := "acc-" + projectName + "-anon-"
	return strings.HasPrefix(volume.Name, prefix) && volume.Labels[accVolumeLabel] == volume.Name
}

// orphanContainerIDs returns the IDs of project service containers for
// services that are no longer declared by the current Compose file.
func orphanContainerIDs(project *types.Project, containers []container.ContainerSummary) []string {
	activeServices := make(map[string]struct{}, len(project.Services))
	for serviceName := range project.Services {
		activeServices[serviceName] = struct{}{}
	}

	ids := make([]string, 0)
	for _, item := range containers {
		if item.Labels[accProjectLabel] != project.Name {
			continue
		}
		serviceName := strings.TrimSpace(item.Labels[accServiceLabel])
		if serviceName == "" {
			continue
		}
		if _, active := activeServices[serviceName]; !active {
			ids = append(ids, item.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

// generateDownServiceOrder returns selected services and their transitive
// dependents in dependency order. Reversing this order tears down dependents
// before the services they rely on.
func generateDownServiceOrder(project *types.Project, selected []string) ([]string, error) {
	if len(selected) == 0 {
		return generateServiceOrder(project, nil)
	}

	dependents := make(map[string][]string, len(project.Services))
	for _, serviceName := range project.ServiceNames() {
		service, err := project.GetService(serviceName)
		if err != nil {
			return nil, err
		}
		for dependencyName := range service.DependsOn {
			dependents[dependencyName] = append(dependents[dependencyName], serviceName)
		}
	}
	for dependencyName := range dependents {
		sort.Strings(dependents[dependencyName])
	}

	selectedSet := make(map[string]struct{})
	var selectDependents func(string) error
	selectDependents = func(serviceName string) error {
		if _, ok := selectedSet[serviceName]; ok {
			return nil
		}
		if _, err := project.GetService(serviceName); err != nil {
			return fmt.Errorf("%w: %s", ErrServiceNotFound, serviceName)
		}
		selectedSet[serviceName] = struct{}{}
		for _, dependentName := range dependents[serviceName] {
			if err := selectDependents(dependentName); err != nil {
				return err
			}
		}
		return nil
	}
	for _, serviceName := range selected {
		if err := selectDependents(serviceName); err != nil {
			return nil, err
		}
	}

	serviceNames := make([]string, 0, len(selectedSet))
	for serviceName := range selectedSet {
		serviceNames = append(serviceNames, serviceName)
	}
	sort.Strings(serviceNames)

	state := make(map[string]int, len(serviceNames))
	order := make([]string, 0, len(serviceNames))
	var visit func(string) error
	visit = func(serviceName string) error {
		if state[serviceName] == 2 {
			return nil
		}
		if state[serviceName] == 1 {
			return fmt.Errorf("cyclic dependency detected at service %q", serviceName)
		}

		service, err := project.GetService(serviceName)
		if err != nil {
			return err
		}
		state[serviceName] = 1
		dependencies := make([]string, 0, len(service.DependsOn))
		for dependencyName := range service.DependsOn {
			if _, ok := selectedSet[dependencyName]; ok {
				dependencies = append(dependencies, dependencyName)
			}
		}
		sort.Strings(dependencies)
		for _, dependencyName := range dependencies {
			if err := visit(dependencyName); err != nil {
				return err
			}
		}
		state[serviceName] = 2
		order = append(order, serviceName)
		return nil
	}
	for _, serviceName := range serviceNames {
		if err := visit(serviceName); err != nil {
			return nil, err
		}
	}
	return order, nil
}

func downContainerIDs(project *types.Project, services []string, containers []container.ContainerSummary, removeOrphans bool) []string {
	activeServices := make(map[string]struct{}, len(project.Services))
	for serviceName := range project.Services {
		activeServices[serviceName] = struct{}{}
	}

	requestedServices := make(map[string]struct{}, len(services))
	for _, serviceName := range services {
		requestedServices[serviceName] = struct{}{}
	}

	ranks := make(map[string]int, len(services))
	for i := len(services) - 1; i >= 0; i-- {
		ranks[services[i]] = len(services) - 1 - i
	}

	type candidate struct {
		id      string
		service string
		rank    int
	}
	candidates := make([]candidate, 0, len(containers))
	for _, item := range containers {
		if item.Labels[accProjectLabel] != project.Name {
			continue
		}
		serviceName := item.Labels[accServiceLabel]
		if strings.TrimSpace(serviceName) == "" {
			// Project infrastructure is not a Compose service or orphan. Its
			// lifecycle is coordinated outside the Compose SDK.
			continue
		}
		_, isActive := activeServices[serviceName]
		_, isRequested := requestedServices[serviceName]
		isOrphan := !isActive
		if !isRequested && !(removeOrphans && isOrphan) {
			continue
		}

		rank, ok := ranks[serviceName]
		if !ok {
			rank = len(services)
		}
		candidates = append(candidates, candidate{id: item.ID, service: serviceName, rank: rank})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].rank != candidates[j].rank {
			return candidates[i].rank < candidates[j].rank
		}
		if candidates[i].service != candidates[j].service {
			return candidates[i].service < candidates[j].service
		}
		return candidates[i].id < candidates[j].id
	})

	ids := make([]string, 0, len(candidates))
	for _, item := range candidates {
		ids = append(ids, item.id)
	}
	return ids
}

// isNotFoundLikeError normalizes runtime errors for idempotent down operations.
func isNotFoundLikeError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") || strings.Contains(msg, "no such")
}
