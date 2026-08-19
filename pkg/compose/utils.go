package compose

import (
	"fmt"
	"sort"

	"github.com/compose-spec/compose-go/v2/types"
)

const (
	accProjectLabel = "io.github.kunalvirwal.acc.project"
	accServiceLabel = "io.github.kunalvirwal.acc.service"
)

// containerName returns the deterministic container name used for a service.
func containerName(projectName string, serviceName string) string {
	return fmt.Sprintf("%s_%s_1", projectName, serviceName)
}

// serviceOrder returns services in dependency order using DFS-based topological sorting.
func generateServiceOrder(project *types.Project, selected []string) ([]string, error) {
	if project == nil {
		return nil, fmt.Errorf("project cannot be nil")
	}

	names := selected
	if len(names) == 0 {
		names = project.ServiceNames()
	}

	state := map[string]int{}
	order := make([]string, 0, len(names))
	for _, name := range names {
		if err := visitService(project, name, state, &order); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// visitService walks dependencies and detects cycles.
func visitService(project *types.Project, name string, state map[string]int, order *[]string) error {
	if state[name] == 2 {
		return nil
	}
	if state[name] == 1 {
		return fmt.Errorf("cyclic dependency detected at service %q", name)
	}

	svc, err := project.GetService(name)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrServiceNotFound, name)
	}

	state[name] = 1
	dependencies := make([]string, 0, len(svc.DependsOn))
	for depName := range svc.DependsOn {
		dependencies = append(dependencies, depName)
	}
	sort.Strings(dependencies)
	for _, depName := range dependencies {
		if err := visitService(project, depName, state, order); err != nil {
			return err
		}
	}

	state[name] = 2
	for _, existing := range *order {
		if existing == name {
			return nil
		}
	}
	*order = append(*order, name)
	return nil
}
