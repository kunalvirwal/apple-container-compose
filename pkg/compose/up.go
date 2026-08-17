package compose

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/kunalvirwal/apple-container-compose/pkg/container"
)

// UpOptions controls compose up behavior.
type UpOptions struct {
	// Services limits startup to the selected services. Empty means all services.
	Services []string
	// Build triggers image build before starting containers.
	Build bool
	// Output receives build output and attached service logs when enabled.
	Output io.Writer
	// Attach streams service logs until detached or context cancellation.
	Attach bool
	// Detach, when non-nil, detaches attached log streaming when signaled.
	// Canceling the context also detaches.
	Detach <-chan struct{}
}

// Up brings selected services up in dependency order.
// It can optionally build images before creating and starting containers.
func (c *ComposeClient) Up(ctx context.Context, path string, parseOpts ParseOptions, opts UpOptions) error {
	if c.containerClient == nil {
		return ErrContainerClientNil
	}

	project, err := c.loadProject(ctx, path, parseOpts)
	if err != nil {
		return err
	}

	if opts.Build {
		if err := c.BuildImages(ctx, path, parseOpts, BuildOptions{Services: opts.Services, Output: opts.Output}); err != nil {
			return err
		}
	}

	services, err := generateServiceOrder(project, opts.Services)
	if err != nil {
		return err
	}

	for _, serviceName := range services {
		svc, err := project.GetService(serviceName)
		if err != nil {
			return err
		}

		image := svc.Image
		if image == "" {
			if svc.Build == nil {
				return fmt.Errorf("service %q has neither image nor build", serviceName)
			}
			image = containerName(project.Name, serviceName)
		}

		name := containerName(project.Name, serviceName)
		createOpts, err := toCreateOptions(svc, name)
		if err != nil {
			return err
		}

		if _, err := c.containerClient.Container.Run(ctx, image, createOpts); err != nil {
			if svc.Build != nil && !opts.Build && isImagePullFailure(err) {
				if buildErr := c.BuildImages(ctx, path, parseOpts, BuildOptions{Services: []string{serviceName}, Output: opts.Output}); buildErr != nil {
					return buildErr
				}
				if _, retryErr := c.containerClient.Container.Run(ctx, image, createOpts); retryErr != nil {
					if !isAlreadyRunningOrExisting(retryErr) {
						return retryErr
					}
				}
				continue
			}
			if !isAlreadyRunningOrExisting(err) {
				return err
			}
		}
	}

	if opts.Attach {
		if err := c.streamLogsUntilDetach(ctx, project.Name, services, opts); err != nil {
			return err
		}
	}

	return nil
}

// streamLogsUntilDetach streams logs for selected services and detaches on signal.
func (c *ComposeClient) streamLogsUntilDetach(ctx context.Context, projectName string, services []string, opts UpOptions) error {
	if len(services) == 0 {
		return nil
	}

	output := opts.Output
	if output == nil {
		output = io.Discard
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, len(services))
	var wg sync.WaitGroup
	sharedWriter := &synchronizedWriter{w: output}

	for _, serviceName := range services {
		containerID := containerName(projectName, serviceName)
		wg.Add(1)
		go func() {
			defer wg.Done()
			prefixed := newPrefixedWriter(sharedWriter, "["+serviceName+"] ")
			_, err := c.containerClient.Container.Logs(streamCtx, container.LogsOptions{
				Follow: true,
				IDs:    []string{containerID},
			}, prefixed)
			if err != nil && !errors.Is(err, context.Canceled) {
				errCh <- err
			}
		}()
	}

	select {
	case <-ctx.Done():
		cancel()
	case err := <-errCh:
		cancel()
		wg.Wait()
		return err
	case <-opts.Detach:
		cancel()
	}

	wg.Wait()
	return nil
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
func toCreateOptions(service types.ServiceConfig, name string) (container.CreateOptions, error) {
	createOpts := container.CreateOptions{
		Name:        name,
		Environment: serviceEnvironmentToList(service.Environment),
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

// isImagePullFailure checks common image pull/lookup failures from container run.
func isImagePullFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "pull access denied") ||
		strings.Contains(msg, "failed to resolve") ||
		strings.Contains(msg, "failed to pull") ||
		strings.Contains(msg, "manifest unknown") ||
		strings.Contains(msg, "not found") ||
		strings.Contains(msg, "no such image")
}
