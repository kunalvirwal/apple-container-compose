package compose

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/kunalvirwal/apple-container-compose/pkg/container"
)

// LogsOptions controls which service logs are displayed and whether they are followed.
type LogsOptions struct {
	// Services limits logs to selected services. Empty means all services.
	Services []string
	// Follow keeps streaming logs until the context is cancelled or the process exits.
	Follow bool
	// Output receives the service logs.
	Output io.Writer
}

// Logs retrieves logs for selected services in a Compose project.
func (c *ComposeClient) Logs(ctx context.Context, path string, parseOpts ParseOptions, opts LogsOptions) error {
	if c.containerClient == nil {
		return ErrContainerClientNil
	}

	project, err := c.loadProject(ctx, path, parseOpts)
	if err != nil {
		return err
	}

	services := opts.Services
	if len(services) == 0 {
		services = project.ServiceNames()
	}

	for _, serviceName := range services {
		if _, err := project.GetService(serviceName); err != nil {
			return err
		}
	}
	if len(services) == 0 {
		return nil
	}

	output := opts.Output
	if output == nil {
		output = io.Discard
	}
	return c.streamServiceLogs(ctx, project.Name, services, opts.Follow, output)
}

// streamServiceLogs forwards each service stream with a stable service prefix.
func (c *ComposeClient) streamServiceLogs(ctx context.Context, projectName string, services []string, follow bool, output io.Writer) error {
	errCh := make(chan error, len(services))
	var wg sync.WaitGroup
	sharedWriter := &synchronizedWriter{mu: &sync.Mutex{}, w: output}

	for _, serviceName := range services {
		containerID := containerName(projectName, serviceName)
		wg.Add(1)
		go func(name, id string) {
			defer wg.Done()
			prefixed := newPrefixedWriter(sharedWriter, "["+name+"] ")
			_, err := c.containerClient.Container.Logs(ctx, container.LogsOptions{
				Follow: follow,
				IDs:    []string{id},
			}, prefixed)
			if err != nil && !errors.Is(err, context.Canceled) {
				errCh <- err
			}
		}(serviceName, containerID)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		return err
	}
	return nil
}
