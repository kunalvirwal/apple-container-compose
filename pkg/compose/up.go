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
}

// Up brings selected services up in dependency order. It builds missing local
// images for services that declare build configuration before starting them.
func (c *ComposeClient) Up(ctx context.Context, path string, parseOpts ParseOptions, opts UpOptions) error {
	if c.containerClient == nil {
		return ErrContainerClientNil
	}

	project, err := c.loadProject(ctx, path, parseOpts)
	if err != nil {
		return err
	}

	buildOutput := opts.BuildOutput
	if buildOutput == nil {
		buildOutput = opts.Output
	}

	services, err := generateServiceOrder(project, opts.Services)
	if err != nil {
		return err
	}
	if err := c.resolveServiceImages(ctx, project, services, opts.Build, buildOutput); err != nil {
		return err
	}
	var logSession *serviceLogSession
	if opts.Attach {
		logSession = newServiceLogSession(c, ctx, opts.Output, len(services))
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
			if !isAlreadyRunningOrExisting(err) {
				return err
			}
		}
		if logSession != nil {
			logSession.Start(project.Name, serviceName)
		}
	}

	if logSession != nil {
		return logSession.Wait(opts.Detach)
	}

	return nil
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
