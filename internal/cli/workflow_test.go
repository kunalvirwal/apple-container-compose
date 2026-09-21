package cli

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
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
		Volumes:   container.NewVolumeClient(run),
		Networks:  container.NewNetworkClient(run),
	}))
	if err != nil {
		t.Fatal(err)
	}
	starter := &recordingDNSStarter{
		dnsByNetwork: map[string]netip.Addr{"demo_default": netip.MustParseAddr("192.168.64.2")},
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
	for _, call := range calls {
		if reflect.DeepEqual(call, want) {
			return true
		}
	}
	return false
}
