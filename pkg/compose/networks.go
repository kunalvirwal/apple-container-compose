package compose

import (
	"context"
	"fmt"
	"reflect"
	"sort"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/kunalvirwal/apple-container-compose/pkg/container"
)

type composeNetwork struct {
	logicalName string
	runtimeName string
	external    bool
}

// validateNetworks accepts plain Compose networks and returns their resolved
// runtime names for each selected service. Advanced network configuration is
// rejected until it can be represented accurately by Apple Container.
func validateNetworks(project *types.Project, services []string, onFatalWarning func(string)) (map[string]composeNetwork, error) {
	if project == nil {
		return nil, fmt.Errorf("project cannot be nil")
	}

	names := make([]string, 0, len(project.Networks))
	for name := range project.Networks {
		names = append(names, name)
	}
	sort.Strings(names)
	networks := make(map[string]composeNetwork, len(names))
	for _, logicalName := range names {
		config := project.Networks[logicalName]
		if config.Name == "" {
			return nil, fatalUnsupportedFeature(onFatalWarning, fmt.Sprintf("network %q has no runtime name", logicalName))
		}

		unsupported := config
		unsupported.Name = ""
		unsupported.External = false
		if !reflect.DeepEqual(unsupported, types.NetworkConfig{}) {
			return nil, fatalUnsupportedFeature(onFatalWarning, fmt.Sprintf("network %q configuration is not supported by ACC", logicalName))
		}
		networks[logicalName] = composeNetwork{
			logicalName: logicalName,
			runtimeName: config.Name,
			external:    bool(config.External),
		}
	}

	for _, serviceName := range services {
		service, err := project.GetService(serviceName)
		if err != nil {
			return nil, err
		}
		if service.NetworkMode != "" {
			return nil, fatalUnsupportedFeature(onFatalWarning, fmt.Sprintf("service %q network_mode is not supported by ACC", serviceName))
		}
		for logicalName, config := range service.Networks {
			if _, exists := networks[logicalName]; !exists {
				return nil, fatalUnsupportedFeature(onFatalWarning, fmt.Sprintf("service %q refers to undefined network %q", serviceName, logicalName))
			}
			if config == nil {
				continue
			}
			unsupported := *config
			unsupported.Aliases = nil
			if !reflect.DeepEqual(unsupported, types.ServiceNetworkConfig{}) {
				return nil, fatalUnsupportedFeature(onFatalWarning, fmt.Sprintf("service %q network %q options are not supported by ACC", serviceName, logicalName))
			}
		}
	}
	return networks, nil
}

// validateNetworkOwnership checks runtime network collisions and required
// external networks before Up creates any project resources.
func (c *ComposeClient) validateNetworkOwnership(ctx context.Context, project *types.Project, networks map[string]composeNetwork, onFatalWarning func(string)) error {
	logicalNames := make([]string, 0, len(networks))
	for name := range networks {
		logicalNames = append(logicalNames, name)
	}
	sort.Strings(logicalNames)
	for _, logicalName := range logicalNames {
		network := networks[logicalName]
		summary, exists, err := c.containerClient.Networks.InspectSummary(ctx, network.runtimeName)
		if err != nil {
			return fmt.Errorf("inspect network %q: %w", network.runtimeName, err)
		}
		if network.external {
			if !exists {
				return fmt.Errorf("external network %q does not exist", network.runtimeName)
			}
			continue
		}
		if exists && (summary.Labels[accProjectLabel] != project.Name || summary.Labels[accNetworkLabel] != logicalName) {
			return fatalUnsupportedFeature(onFatalWarning, fmt.Sprintf("network %q already exists but is not owned by ACC project %q; choose a different project name", network.runtimeName, project.Name))
		}
	}
	return nil
}

// ensureNetworks creates project-managed networks and returns sorted runtime
// network names for every service.
func (c *ComposeClient) ensureNetworks(ctx context.Context, project *types.Project, networks map[string]composeNetwork, onFatalWarning func(string)) (map[string][]string, error) {
	if project == nil {
		return nil, fmt.Errorf("project cannot be nil")
	}

	logicalNames := make([]string, 0, len(networks))
	for name := range networks {
		logicalNames = append(logicalNames, name)
	}
	sort.Strings(logicalNames)
	for _, logicalName := range logicalNames {
		network := networks[logicalName]
		summary, exists, err := c.containerClient.Networks.InspectSummary(ctx, network.runtimeName)
		if err != nil {
			return nil, fmt.Errorf("inspect network %q: %w", network.runtimeName, err)
		}
		if network.external {
			if !exists {
				return nil, fmt.Errorf("external network %q does not exist", network.runtimeName)
			}
			continue
		}
		if exists {
			if summary.Labels[accProjectLabel] != project.Name || summary.Labels[accNetworkLabel] != logicalName {
				return nil, fatalUnsupportedFeature(onFatalWarning, fmt.Sprintf("network %q already exists but is not owned by ACC project %q; choose a different project name", network.runtimeName, project.Name))
			}
			continue
		}
		if _, err := c.containerClient.Networks.Create(ctx, container.NetworkCreateOptions{
			Name: network.runtimeName,
			Labels: map[string]string{
				accNetworkCoreDNSLabel: "false",
				accNetworkLabel:        logicalName,
				accProjectLabel:        project.Name,
			},
		}); err != nil {
			return nil, fmt.Errorf("create network %q: %w", network.runtimeName, err)
		}
	}

	return serviceNetworkNames(project, networks), nil
}

func serviceNetworkNames(project *types.Project, networks map[string]composeNetwork) map[string][]string {
	serviceNetworks := make(map[string][]string, len(project.Services))
	for serviceName, service := range project.Services {
		logicalServiceNetworks := make([]string, 0, len(service.Networks))
		for logicalName := range service.Networks {
			logicalServiceNetworks = append(logicalServiceNetworks, logicalName)
		}
		sort.Strings(logicalServiceNetworks)
		runtimeNames := make([]string, 0, len(logicalServiceNetworks))
		for _, logicalName := range logicalServiceNetworks {
			runtimeNames = append(runtimeNames, networks[logicalName].runtimeName)
		}
		serviceNetworks[serviceName] = runtimeNames
	}
	return serviceNetworks
}
