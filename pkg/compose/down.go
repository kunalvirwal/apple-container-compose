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
	// Services limits teardown to selected services. Empty means all services.
	Services []string
	// RemoveOrphans also removes project containers whose service is no longer
	// declared in the current Compose file.
	RemoveOrphans bool
	// Force forces container deletion when supported by the runtime.
	Force bool
	// Volumes removes named volumes created for the project. As with Docker
	// Compose, named volumes are retained unless this option is set.
	Volumes bool
}

// Down stops and removes labeled project containers in reverse dependency order.
func (c *ComposeClient) Down(ctx context.Context, path string, parseOpts ParseOptions, opts DownOptions) error {
	if c.containerClient == nil {
		return ErrContainerClientNil
	}

	project, err := c.loadProject(ctx, path, parseOpts)
	if err != nil {
		return err
	}

	services, err := generateDownServiceOrder(project, opts.Services)
	if err != nil {
		return err
	}

	containers, err := c.containerClient.Container.ListSummaries(ctx, container.ListOptions{All: true})
	if err != nil {
		return err
	}

	ids := downContainerIDs(project, services, containers, opts.RemoveOrphans)
	attachedVolumeNames := make(map[string]struct{})
	if opts.Volumes && len(opts.Services) == 0 && len(ids) > 0 {
		details, err := c.containerClient.Container.InspectDetails(ctx, ids)
		if err != nil {
			if !isNotFoundLikeError(err) {
				return err
			}
		} else {
			for _, item := range details {
				for _, name := range item.VolumeNames {
					attachedVolumeNames[name] = struct{}{}
				}
			}
		}
	}
	if len(ids) > 0 {
		if _, err := c.containerClient.Container.Stop(ctx, container.StopOptions{IDs: ids}); err != nil {
			if !isNotFoundLikeError(err) {
				return err
			}
		}

		if _, err := c.containerClient.Container.Delete(ctx, container.DeleteOptions{IDs: ids, Force: opts.Force}); err != nil {
			if !isNotFoundLikeError(err) {
				return err
			}
		}
	}

	if opts.Volumes && len(opts.Services) == 0 {
		if err := c.removeProjectVolumes(ctx, project.Name, attachedVolumeNames); err != nil {
			return err
		}
	}
	if len(opts.Services) == 0 && !hasRemainingProjectContainers(project.Name, containers, ids) {
		if err := c.removeProjectNetworks(ctx, project.Name); err != nil {
			return err
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

func hasRemainingProjectContainers(projectName string, containers []container.ContainerSummary, removedIDs []string) bool {
	removed := make(map[string]struct{}, len(removedIDs))
	for _, id := range removedIDs {
		removed[id] = struct{}{}
	}
	for _, item := range containers {
		if item.Labels[accProjectLabel] == projectName {
			if _, willRemove := removed[item.ID]; !willRemove {
				return true
			}
		}
	}
	return false
}

// removeProjectVolumes removes all project-managed named volumes plus the
// explicitly-created anonymous volumes mounted by containers removed in this
// Down invocation. Detached anonymous volumes are retained: without their
// former container there is no safe association with this teardown.
func (c *ComposeClient) removeProjectVolumes(ctx context.Context, projectName string, attachedVolumeNames map[string]struct{}) error {
	volumes, err := c.containerClient.Volumes.ListSummaries(ctx)
	if err != nil {
		return err
	}

	names := make([]string, 0)
	for _, volume := range volumes {
		if volume.Labels[accProjectLabel] != projectName || volume.Labels[accVolumeLabel] == "" {
			continue
		}
		if isAnonymousVolume(projectName, volume) {
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
