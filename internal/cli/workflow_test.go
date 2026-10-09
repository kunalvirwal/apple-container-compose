package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kunalvirwal/apple-container-compose/pkg/compose"
	"github.com/kunalvirwal/apple-container-compose/pkg/container"
)

func TestUpRoutesRuntimeProgressThroughBlueWriter(t *testing.T) {
	for _, noColor := range []bool{false, true} {
		name := "colored"
		if noColor {
			name = "no color"
		}
		t.Run(name, func(t *testing.T) {
			path := writeWorkflowCompose(t)
			detach := make(chan struct{})
			run := func(_ context.Context, args ...string) (string, error) {
				if reflect.DeepEqual(args, []string{"network", "inspect", "demo_default"}) {
					return "network not found", errors.New("command failed")
				}
				return "", nil
			}
			progress := []string{
				"[0/6] [0s]\n",
				"[1/6] Fetching image [0s]\n",
				"[2/6] Unpacking image [0s]\n",
				"[3/6] Fetching kernel [0s]\n",
				"[4/6] Fetching init image [0s]\n",
				"[5/6] Unpacking init image [0s]\n",
				"[6/6] Starting container [0s]\n",
			}
			stream := func(ctx context.Context, output io.Writer, args ...string) (string, error) {
				if len(args) > 0 && args[0] == "run" {
					for _, line := range progress {
						if _, err := io.WriteString(output, line); err != nil {
							return "", err
						}
					}
					return strings.Join(progress, ""), nil
				}
				if _, err := io.WriteString(output, "app-ready\n"); err != nil {
					return "", err
				}
				close(detach)
				<-ctx.Done()
				return "app-ready\n", ctx.Err()
			}
			runtime := &container.Client{
				Container: container.NewContainerClient(run, stream),
				Images:    container.NewImageClient(run, nil),
				Volumes:   container.NewVolumeClient(run),
				Networks:  container.NewNetworkClient(run),
			}
			client, err := compose.NewComposeClient(compose.WithContainerClient(runtime))
			if err != nil {
				t.Fatal(err)
			}
			dns := &recordingDNSStarter{dnsByNetwork: map[string]netip.Addr{"demo_default": netip.MustParseAddr("192.168.64.2")}}
			root := newRootCommand(
				func() (composeService, error) { return client, nil },
				func(ctx context.Context, path string, parseOpts compose.ParseOptions, opts compose.UpOptions, _ bool) error {
					opts.Detach = detach
					return startServicesWithCoreDNS(ctx, client, dns, path, parseOpts, opts, newUpReporter(runtime, opts.Output))
				},
				func(context.Context, string, compose.ParseOptions, compose.DownOptions) error { return nil },
			)
			var output bytes.Buffer
			root.SetOut(&output)
			args := []string{"up", "--file", path}
			if noColor {
				args = append(args, "--no-color")
			}
			root.SetArgs(args)
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			for _, line := range progress {
				want := line
				if !noColor {
					want = ansiBlue + line + ansiReset
				}
				if !strings.Contains(output.String(), want) {
					t.Fatalf("runtime progress missing correct color %q in %q", want, output.String())
				}
			}
			if !strings.Contains(output.String(), "[app] app-ready\n") {
				t.Fatalf("attached service logs missing in %q", output.String())
			}
			preparing := "[ACC] Preparing Compose project with ACC CoreDNS\n"
			if !noColor {
				preparing = ansiPurple + preparing + ansiReset
			}
			if !strings.Contains(output.String(), preparing) {
				t.Fatalf("ACC event missing correct color %q in %q", preparing, output.String())
			}
			if noColor && strings.Contains(output.String(), "\x1b[") {
				t.Fatalf("no-color output contains ANSI styling: %q", output.String())
			}
			t.Logf("captured output: %q", output.String())
		})
	}
}

func TestStartServicesWithCoreDNSStartsCoreDNSBeforeServices(t *testing.T) {
	composePath := writeWorkflowCompose(t)
	var calls [][]string
	run := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		if reflect.DeepEqual(args, []string{"network", "inspect", "demo_default"}) {
			return "network not found", errors.New("command failed")
		}
		return "", nil
	}
	composeClient, err := compose.NewComposeClient(compose.WithContainerClient(&container.Client{
		Container: container.NewContainerClient(run, nil),
		Images:    container.NewImageClient(run, nil),
		Volumes:   container.NewVolumeClient(run),
		Networks:  container.NewNetworkClient(run),
	}))
	if err != nil {
		t.Fatal(err)
	}
	starter := &recordingDNSStarter{
		dnsByNetwork: map[string]netip.Addr{"demo_default": netip.MustParseAddr("192.168.64.2")},
		onFinalize: func() {
			for _, call := range calls {
				if len(call) > 0 && call[0] == "run" {
					return
				}
			}
			t.Fatal("CoreDNS migration finalized before service startup")
		},
		onStart: func() {
			wantCreate := []string{
				"network", "create",
				"--label", "io.github.kunalvirwal.acc.network=default",
				"--label", "io.github.kunalvirwal.acc.network.coredns=false",
				"--label", "io.github.kunalvirwal.acc.project=demo",
				"demo_default",
			}
			if !containsCommand(calls, wantCreate) {
				t.Fatalf("CoreDNS started before network creation; calls = %#v", calls)
			}
			for _, call := range calls {
				if len(call) > 0 && call[0] == "run" {
					t.Fatalf("service started before CoreDNS; calls = %#v", calls)
				}
			}
		},
	}
	if err := startServicesWithCoreDNS(context.Background(), composeClient, starter, composePath, compose.ParseOptions{}, compose.UpOptions{}); err != nil {
		t.Fatalf("startServicesWithCoreDNS() error = %v", err)
	}
	if starter.project != "demo" || !reflect.DeepEqual(starter.networks, []string{"demo_default"}) {
		t.Fatalf("CoreDNS start = project %q, networks %#v", starter.project, starter.networks)
	}
	wantRun := []string{
		"run", "--name", "demo_app_1", "--dns", "192.168.64.2",
		"--label", "io.github.kunalvirwal.acc.project=demo",
		"--label", "io.github.kunalvirwal.acc.service=app",
		"--network", "demo_default", "-d", "alpine",
	}
	if !containsCommand(calls, wantRun) {
		t.Fatalf("runtime calls = %#v, want %#v", calls, wantRun)
	}
}

func TestStartServicesWithoutCoreDNSCleansUpBeforeAttachedLogWait(t *testing.T) {
	composePath := writeWorkflowCompose(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var events []string
	run := func(commandCtx context.Context, args ...string) (string, error) {
		switch {
		case reflect.DeepEqual(args, []string{"network", "inspect", "demo_default"}):
			return `[{"configuration":{"name":"demo_default","labels":{"io.github.kunalvirwal.acc.project":"demo","io.github.kunalvirwal.acc.network":"default"}}}]`, nil
		case reflect.DeepEqual(args, []string{"network", "list", "--format", "json"}):
			return `[{"configuration":{"name":"demo_old","labels":{"io.github.kunalvirwal.acc.project":"demo","io.github.kunalvirwal.acc.network":"old"}}}]`, nil
		case reflect.DeepEqual(args, []string{"network", "delete", "demo_old"}):
			if err := commandCtx.Err(); err != nil {
				t.Errorf("network cleanup inherited canceled log context: %v", err)
			}
			events = append(events, "delete old network")
			return "", nil
		case reflect.DeepEqual(args, []string{"list", "--format", "json", "--all"}), reflect.DeepEqual(args, []string{"list", "--format", "json"}):
			return `[{"configuration":{"id":"demo_app_1","labels":{"io.github.kunalvirwal.acc.project":"demo","io.github.kunalvirwal.acc.service":"app","io.github.kunalvirwal.acc.config-hash":"old"}}}]`, nil
		case reflect.DeepEqual(args, []string{"inspect", "demo_app_1"}):
			return `[{"configuration":{"id":"demo_app_1","mounts":[]},"status":{"networks":[{"network":"demo_old","ipv4Address":"192.168.64.5/24"}]}}]`, nil
		case reflect.DeepEqual(args, []string{"stop", "demo_app_1"}):
			events = append(events, "stop old service")
			return "", nil
		case reflect.DeepEqual(args, []string{"delete", "demo_app_1"}):
			events = append(events, "delete old service")
			return "", nil
		case len(args) > 0 && args[0] == "run":
			if !containsArgument(args, "--dns", "1.1.1.1") {
				t.Errorf("service run lacks direct Compose DNS: %#v", args)
			}
			events = append(events, "run direct DNS service")
			cancel() // Simulate closing attached logs immediately after startup.
			return "", nil
		default:
			return "", nil
		}
	}
	stream := func(ctx context.Context, _ io.Writer, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "run" {
			return run(ctx, args...)
		}
		<-ctx.Done()
		return "", ctx.Err()
	}
	composeClient, err := compose.NewComposeClient(compose.WithContainerClient(&container.Client{
		Container: container.NewContainerClient(run, stream),
		Images:    container.NewImageClient(func(context.Context, ...string) (string, error) { return "", nil }, nil),
		Volumes:   container.NewVolumeClient(run),
		Networks:  container.NewNetworkClient(run),
	}))
	if err != nil {
		t.Fatal(err)
	}
	dns := recordingDirectDNSRemover{onRemove: func() { events = append(events, "delete CoreDNS") }}
	detach := make(chan struct{})
	close(detach)
	err = startServicesWithoutCoreDNS(ctx, composeClient, dns, composePath, compose.ParseOptions{}, compose.UpOptions{Attach: true, Detach: detach})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"stop old service", "delete old service", "run direct DNS service", "delete CoreDNS", "delete old network"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

type recordingDirectDNSRemover struct {
	onRemove func()
}

func (r recordingDirectDNSRemover) RemoveAfterDirectDNS(context.Context, string, []string) (bool, error) {
	if r.onRemove != nil {
		r.onRemove()
	}
	return true, nil
}

func containsArgument(args []string, flag, value string) bool {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == flag && args[index+1] == value {
			return true
		}
	}
	return false
}

func TestDownServicesWithCoreDNSRemovesResourcesInOrder(t *testing.T) {
	composePath := writeWorkflowCompose(t)
	var events []string
	listCalls := 0
	run := func(_ context.Context, args ...string) (string, error) {
		switch {
		case reflect.DeepEqual(args, []string{"list", "--format", "json", "--all"}):
			listCalls++
			if listCalls == 1 {
				return `[
  {"configuration":{"id":"demo_app_1","labels":{"io.github.kunalvirwal.acc.project":"demo","io.github.kunalvirwal.acc.service":"app"}}},
  {"configuration":{"id":"demo_old_1","labels":{"io.github.kunalvirwal.acc.project":"demo","io.github.kunalvirwal.acc.service":"old"}}}
]`, nil
			}
			return "[]", nil
		case reflect.DeepEqual(args, []string{"inspect", "demo_app_1", "demo_old_1"}):
			return `[
  {"configuration":{"id":"demo_app_1","mounts":[{"type":{"volume":{"name":"demo_appdata"}}}]}},
  {"configuration":{"id":"demo_old_1","mounts":[{"type":{"volume":{"name":"demo_olddata"}}}]}}
]`, nil
		case reflect.DeepEqual(args, []string{"volume", "list", "--format", "json"}):
			return `[
  {"id":"demo_appdata","configuration":{"name":"demo_appdata","labels":{"io.github.kunalvirwal.acc.project":"demo","io.github.kunalvirwal.acc.volume":"appdata"}}},
  {"id":"demo_olddata","configuration":{"name":"demo_olddata","labels":{"io.github.kunalvirwal.acc.project":"demo","io.github.kunalvirwal.acc.volume":"olddata"}}}
]`, nil
		case reflect.DeepEqual(args, []string{"volume", "delete", "demo_appdata", "demo_olddata"}):
			events = append(events, "volumes")
			return "", nil
		case reflect.DeepEqual(args, []string{"network", "list", "--format", "json"}):
			return `[{"configuration":{"name":"demo_default","labels":{"io.github.kunalvirwal.acc.project":"demo","io.github.kunalvirwal.acc.network":"default"}}}]`, nil
		case reflect.DeepEqual(args, []string{"network", "delete", "demo_default"}):
			events = append(events, "networks")
			return "", nil
		default:
			return "", nil
		}
	}
	composeClient, err := compose.NewComposeClient(compose.WithContainerClient(&container.Client{
		Container: container.NewContainerClient(run, nil),
		Images:    container.NewImageClient(run, nil),
		Volumes:   container.NewVolumeClient(run),
		Networks:  container.NewNetworkClient(run),
	}))
	if err != nil {
		t.Fatal(err)
	}
	dns := recordingDNSRemover{onRemove: func() { events = append(events, "coredns") }}
	if err := downServicesWithCoreDNS(context.Background(), composeClient, dns, composePath, compose.ParseOptions{}, compose.DownOptions{RemoveOrphans: true, Volumes: true}); err != nil {
		t.Fatalf("downServicesWithCoreDNS() error = %v", err)
	}
	if want := []string{"volumes", "coredns", "networks"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("resource removal order = %#v, want %#v", events, want)
	}
}

type recordingDNSStarter struct {
	calls        int
	project      string
	networks     []string
	dnsByNetwork map[string]netip.Addr
	onStart      func()
	onFinalize   func()
}

type recordingDNSRemover struct {
	onRemove func()
}

func (r recordingDNSRemover) RemoveIfUnused(context.Context, string, bool) error {
	if r.onRemove != nil {
		r.onRemove()
	}
	return nil
}

func (s *recordingDNSStarter) Start(_ context.Context, project string, networks []string) (map[string]netip.Addr, error) {
	s.calls++
	s.project = project
	s.networks = append([]string(nil), networks...)
	if s.onStart != nil {
		s.onStart()
	}
	return s.dnsByNetwork, nil
}

func (s *recordingDNSStarter) FinalizeMigration(context.Context, string, []string) (bool, error) {
	if s.onFinalize != nil {
		s.onFinalize()
	}
	return true, nil
}

func writeWorkflowCompose(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "compose.yaml")
	contents := []byte(`name: demo
services:
  app:
    image: alpine
    dns:
      - 1.1.1.1
`)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func containsCommand(calls [][]string, want []string) bool {
	want = withoutRuntimeConfigHash(want)
	for _, call := range calls {
		if reflect.DeepEqual(withoutRuntimeConfigHash(call), want) {
			return true
		}
	}
	return false
}

func withoutRuntimeConfigHash(args []string) []string {
	result := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		if args[index] == "--label" && index+1 < len(args) && strings.HasPrefix(args[index+1], "io.github.kunalvirwal.acc.config-hash=") {
			index++
			continue
		}
		result = append(result, args[index])
	}
	return result
}
