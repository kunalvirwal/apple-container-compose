package cli

import (
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
	stream := func(ctx context.Context, _ io.Writer, _ ...string) (string, error) {
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
