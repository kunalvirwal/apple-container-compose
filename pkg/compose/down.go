package compose

import (
	"context"
	"strings"

	"github.com/kunalvirwal/apple-container-compose/pkg/container"
)

// DownOptions controls compose down behavior.
type DownOptions struct {
	// Services limits teardown to selected services. Empty means all services.
	Services []string
	// Force forces container deletion when supported by the runtime.
	Force bool
}

// Down stops and removes service containers in reverse dependency order.
func (c *ComposeClient) Down(ctx context.Context, path string, parseOpts ParseOptions, opts DownOptions) error {
	if c.containerClient == nil {
		return ErrContainerClientNil
	}

	project, err := c.loadProject(ctx, path, parseOpts)
	if err != nil {
		return err
	}

	services, err := generateServiceOrder(project, opts.Services)
	if err != nil {
		return err
	}

	ids := make([]string, 0, len(services))
	for i := len(services) - 1; i >= 0; i-- {
		ids = append(ids, containerName(project.Name, services[i]))
	}

	if len(ids) == 0 {
		return nil
	}

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

	return nil
}

// isNotFoundLikeError normalizes runtime errors for idempotent down operations.
func isNotFoundLikeError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") || strings.Contains(msg, "no such")
}
