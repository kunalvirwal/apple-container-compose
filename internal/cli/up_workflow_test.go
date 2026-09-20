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

type recordingDNSStarter struct {
	calls        int
	project      string
	networks     []string
	dnsByNetwork map[string]netip.Addr
	onStart      func()
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
