package compose

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

	"github.com/kunalvirwal/apple-container-compose/pkg/container"
)

func newTestImageClient() container.ImageClient {
	return container.NewImageClient(func(context.Context, ...string) (string, error) {
		return "", nil
	}, nil)
}

func TestUpSessionUsesDNSAddressForEachServiceNetwork(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `name: demo
services:
  api:
    image: alpine
    dns:
      - 1.1.1.1
    networks:
      - backend
  frontend:
    image: alpine
    networks:
      - frontend
networks:
  backend:
  frontend:
`)

	var calls [][]string
	run := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		if len(args) == 3 && args[0] == "network" && args[1] == "inspect" {
			return "network not found", errors.New("command failed")
		}
		return "", nil
	}
	client := &ComposeClient{containerClient: &container.Client{
		Container: container.NewContainerClient(run, nil),
		Images:    newTestImageClient(),
		Volumes:   container.NewVolumeClient(run),
		Networks:  container.NewNetworkClient(run),
	}}

	session, err := client.PrepareUp(context.Background(), composePath, ParseOptions{}, UpOptions{})
	if err != nil {
		t.Fatalf("PrepareUp() error = %v", err)
	}
	if got, want := session.NetworkNames(), []string{"demo_backend", "demo_frontend"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("NetworkNames() = %#v, want %#v", got, want)
	}
	if err := session.StartServices(context.Background(), ServiceStartOptions{DNSByNetwork: map[string]netip.Addr{
		"demo_backend":  netip.MustParseAddr("192.168.64.2"),
		"demo_frontend": netip.MustParseAddr("192.168.65.2"),
	}}); err != nil {
		t.Fatalf("StartServices() error = %v", err)
	}

	for _, want := range [][]string{
		{"run", "--name", "demo_api_1", "--dns", "192.168.64.2", "--label", accProjectLabel + "=demo", "--label", accServiceLabel + "=api", "--network", "demo_backend", "-d", "alpine"},
		{"run", "--name", "demo_frontend_1", "--dns", "192.168.65.2", "--label", accProjectLabel + "=demo", "--label", accServiceLabel + "=frontend", "--network", "demo_frontend", "-d", "alpine"},
	} {
		if !hasCall(calls, want) {
			t.Fatalf("runtime calls = %#v, want %#v", calls, want)
		}
	}
}

func TestUpSessionRejectsIncompleteDNSConfigBeforeStartingServices(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `name: demo
services:
  app:
    image: alpine
`)

	var runs int
	run := func(_ context.Context, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "run" {
			runs++
		}
		if len(args) == 3 && args[0] == "network" && args[1] == "inspect" {
			return "network not found", errors.New("command failed")
		}
		return "", nil
	}
	client := &ComposeClient{containerClient: &container.Client{
		Container: container.NewContainerClient(run, nil),
		Images:    newTestImageClient(),
		Volumes:   container.NewVolumeClient(run),
		Networks:  container.NewNetworkClient(run),
	}}
	session, err := client.PrepareUp(context.Background(), composePath, ParseOptions{}, UpOptions{})
	if err != nil {
		t.Fatal(err)
	}
	err = session.StartServices(context.Background(), ServiceStartOptions{DNSByNetwork: map[string]netip.Addr{
		"wrong_network": netip.MustParseAddr("192.168.64.2"),
	}})
	if !errors.Is(err, ErrInvalidDNSConfig) {
		t.Fatalf("StartServices() error = %v, want ErrInvalidDNSConfig", err)
	}
	if runs != 0 {
		t.Fatalf("service run calls = %d, want 0", runs)
	}
}

func TestUpSessionRegistersComposeNameserversInsteadOfRuntimeDNS(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `name: demo
services:
  api:
    image: alpine
    dns:
      - 1.1.1.1
`)

	run := func(_ context.Context, args ...string) (string, error) {
		switch {
		case reflect.DeepEqual(args, []string{"network", "inspect", "demo_default"}):
			return "network not found", errors.New("command failed")
		case reflect.DeepEqual(args, []string{"inspect", "demo_api_1"}):
			return `[{"configuration":{"id":"demo_api_1","mounts":[]},"status":{"networks":[{"network":"demo_default","ipv4Address":"192.168.64.3/24"}]}}]`, nil
		default:
			return "", nil
		}
	}
	registry := &recordingServiceRegistry{}
	client := &ComposeClient{
		containerClient: &container.Client{
			Container: container.NewContainerClient(run, nil),
			Images:    newTestImageClient(),
			Volumes:   container.NewVolumeClient(run),
			Networks:  container.NewNetworkClient(run),
		},
		registry: registry,
	}

	session, err := client.PrepareUp(context.Background(), composePath, ParseOptions{}, UpOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.StartServices(context.Background(), ServiceStartOptions{DNSByNetwork: map[string]netip.Addr{
		"demo_default": netip.MustParseAddr("192.168.64.2"),
	}}); err != nil {
		t.Fatal(err)
	}
	if len(registry.upserts) != 1 {
		t.Fatalf("registry upserts = %#v, want one", registry.upserts)
	}
	if got, want := registry.upserts[0].Nameservers, []string{"1.1.1.1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("registry nameservers = %#v, want %#v", got, want)
	}
}

func TestUpReconcilesExistingServiceState(t *testing.T) {
	baseOptions := container.CreateOptions{
		Name: "demo_app_1",
		Labels: map[string]string{
			accProjectLabel: "demo",
			accServiceLabel: "app",
		},
		Networks: []string{"demo_default"},
	}
	matchingHash, err := serviceConfigHash("alpine", baseOptions)
	if err != nil {
		t.Fatal(err)
	}
	changedOptions := baseOptions
	changedOptions.Environment = []string{"MODE=old"}
	changedHash, err := serviceConfigHash("alpine", changedOptions)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		running    bool
		configHash string
		want       [][]string
		forbid     []string
	}{
		{
			name:       "reuse matching running container",
			running:    true,
			configHash: matchingHash,
			forbid:     []string{"start", "stop", "delete", "run"},
		},
		{
			name:       "start matching stopped container",
			configHash: matchingHash,
			want:       [][]string{{"start", "demo_app_1"}},
			forbid:     []string{"stop", "delete", "run"},
		},
		{
			name:       "recreate changed running container",
			running:    true,
			configHash: changedHash,
			want: [][]string{
				{"stop", "demo_app_1"},
				{"delete", "demo_app_1"},
			},
			forbid: []string{"start"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, composePath := writeVolumeCompose(t, `name: demo
services:
  app:
    image: alpine
`)
			var calls [][]string
			run := func(_ context.Context, args ...string) (string, error) {
				calls = append(calls, append([]string(nil), args...))
				switch {
				case reflect.DeepEqual(args, []string{"list", "--format", "json", "--all"}):
					return `[{"configuration":{"id":"demo_app_1","labels":{"` + accProjectLabel + `":"demo","` + accServiceLabel + `":"app","` + accConfigHashLabel + `":"` + tt.configHash + `"}}}]`, nil
				case reflect.DeepEqual(args, []string{"list", "--format", "json"}):
					if tt.running {
						return `[{"configuration":{"id":"demo_app_1"}}]`, nil
					}
					return "[]", nil
				case reflect.DeepEqual(args, []string{"inspect", "demo_app_1"}):
					return `[{"configuration":{"id":"demo_app_1","mounts":[]}}]`, nil
				default:
					return "", nil
				}
			}
			networkRun := func(_ context.Context, args ...string) (string, error) {
				switch {
				case reflect.DeepEqual(args, []string{"network", "inspect", "demo_default"}):
					return `[{"configuration":{"name":"demo_default","labels":{"` + accProjectLabel + `":"demo","` + accNetworkLabel + `":"default"}}}]`, nil
				case reflect.DeepEqual(args, []string{"network", "list", "--format", "json"}):
					return "[]", nil
				default:
					return "", nil
				}
			}
			client := &ComposeClient{containerClient: &container.Client{
				Container: container.NewContainerClient(run, nil),
				Images:    newTestImageClient(),
				Volumes:   container.NewVolumeClient(run),
				Networks:  container.NewNetworkClient(networkRun),
			}}
			if err := client.Up(context.Background(), composePath, ParseOptions{}, UpOptions{}); err != nil {
				t.Fatalf("Up() error = %v", err)
			}
			for _, want := range tt.want {
				if !hasCall(calls, want) {
					t.Fatalf("calls = %#v, want %#v", calls, want)
				}
			}
			if tt.name == "recreate changed running container" {
				runs := 0
				for _, call := range calls {
					if len(call) > 0 && call[0] == "run" {
						runs++
					}
				}
				if runs != 1 {
					t.Fatalf("run calls = %d, want 1; calls = %#v", runs, calls)
				}
			}
			for _, forbidden := range tt.forbid {
				for _, call := range calls {
					if len(call) > 0 && call[0] == forbidden {
						t.Fatalf("calls = %#v, must not invoke %q", calls, forbidden)
					}
				}
			}
		})
	}
}

func TestUpRemoveOrphansRemovesOnlyUndeclaredServices(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `name: demo
services:
  app:
    image: alpine
`)
	var calls [][]string
	run := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		switch {
		case reflect.DeepEqual(args, []string{"list", "--format", "json", "--all"}),
			reflect.DeepEqual(args, []string{"list", "--format", "json"}):
			return `[{"configuration":{"id":"demo_old_1","labels":{"` + accProjectLabel + `":"demo","` + accServiceLabel + `":"old"}}}]`, nil
		default:
			return "", nil
		}
	}
	networkRun := func(_ context.Context, args ...string) (string, error) {
		switch {
		case reflect.DeepEqual(args, []string{"network", "inspect", "demo_default"}):
			return `[{"configuration":{"name":"demo_default","labels":{"` + accProjectLabel + `":"demo","` + accNetworkLabel + `":"default"}}}]`, nil
		case reflect.DeepEqual(args, []string{"network", "list", "--format", "json"}):
			return "[]", nil
		default:
			return "", nil
		}
	}
	client := &ComposeClient{containerClient: &container.Client{
		Container: container.NewContainerClient(run, nil),
		Images:    newTestImageClient(),
		Volumes:   container.NewVolumeClient(run),
		Networks:  container.NewNetworkClient(networkRun),
	}}
	if err := client.Up(context.Background(), composePath, ParseOptions{}, UpOptions{RemoveOrphans: true}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	for _, want := range [][]string{{"stop", "demo_old_1"}, {"delete", "demo_old_1"}} {
		if !hasCall(calls, want) {
			t.Fatalf("calls = %#v, want %#v", calls, want)
		}
	}
	for _, call := range calls {
		if len(call) > 1 && (call[0] == "stop" || call[0] == "delete") && call[1] == "demo_app_1" {
			t.Fatalf("calls = %#v, must not remove declared service", calls)
		}
	}
}

func TestUpBuildRecreatesServiceWhenBuiltImageChanges(t *testing.T) {
	projectDir, composePath := writeVolumeCompose(t, `name: demo
services:
  app:
    image: demo-app:latest
    build: .
`)
	if err := os.WriteFile(filepath.Join(projectDir, "Dockerfile"), []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	baseOptions := container.CreateOptions{
		Name: "demo_app_1",
		Labels: map[string]string{
			accProjectLabel: "demo",
			accServiceLabel: "app",
		},
		Networks: []string{"demo_default"},
	}
	configHash, err := serviceConfigHash("demo-app:latest", baseOptions)
	if err != nil {
		t.Fatal(err)
	}

	var calls [][]string
	run := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		switch {
		case reflect.DeepEqual(args, []string{"list", "--format", "json", "--all"}),
			reflect.DeepEqual(args, []string{"list", "--format", "json"}):
			return `[{"configuration":{"id":"demo_app_1","labels":{"` + accProjectLabel + `":"demo","` + accServiceLabel + `":"app","` + accConfigHashLabel + `":"` + configHash + `","` + accImageIDLabel + `":"sha256:old"}}}]`, nil
		case reflect.DeepEqual(args, []string{"inspect", "demo_app_1"}):
			return `[{"configuration":{"id":"demo_app_1","mounts":[]}}]`, nil
		default:
			return "", nil
		}
	}
	runStreaming := func(_ context.Context, _ io.Writer, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		return "sha256:new\n", nil
	}
	networkRun := func(_ context.Context, args ...string) (string, error) {
		switch {
		case reflect.DeepEqual(args, []string{"network", "inspect", "demo_default"}):
			return `[{"configuration":{"name":"demo_default","labels":{"` + accProjectLabel + `":"demo","` + accNetworkLabel + `":"default"}}}]`, nil
		case reflect.DeepEqual(args, []string{"network", "list", "--format", "json"}):
			return "[]", nil
		default:
			return "", nil
		}
	}
	client := &ComposeClient{containerClient: &container.Client{
		Container: container.NewContainerClient(run, nil),
		Images:    container.NewImageClient(run, runStreaming),
		Volumes:   container.NewVolumeClient(run),
		Networks:  container.NewNetworkClient(networkRun),
	}}
	if err := client.Up(context.Background(), composePath, ParseOptions{}, UpOptions{Build: true}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	for _, want := range [][]string{{"stop", "demo_app_1"}, {"delete", "demo_app_1"}} {
		if !hasCall(calls, want) {
			t.Fatalf("calls = %#v, want %#v", calls, want)
		}
	}
	foundRun := false
	for _, call := range calls {
		if len(call) > 0 && call[0] == "run" {
			foundRun = true
			if !hasArgument(call, accImageIDLabel+"=sha256:new") {
				t.Fatalf("run args = %#v, want new built image identity label", call)
			}
		}
	}
	if !foundRun {
		t.Fatalf("calls = %#v, want service recreation", calls)
	}
}

func TestUpImageOnlyReconcilesLocalImageIdentity(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `name: demo
services:
  app:
    image: demo-app:latest
`)
	configHash, err := serviceConfigHash("demo-app:latest", container.CreateOptions{
		Name: "demo_app_1",
		Labels: map[string]string{
			accProjectLabel: "demo",
			accServiceLabel: "app",
		},
		Networks: []string{"demo_default"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name       string
		oldID      string
		wantCreate bool
	}{
		{name: "tag now references another image", oldID: "sha256:old", wantCreate: true},
		{name: "tag still references same image", oldID: "sha256:new"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls [][]string
			run := func(_ context.Context, args ...string) (string, error) {
				calls = append(calls, append([]string(nil), args...))
				switch {
				case reflect.DeepEqual(args, []string{"image", "inspect", "demo-app:latest"}):
					return `{"descriptor":{"digest":"sha256:new"}}`, nil
				case reflect.DeepEqual(args, []string{"list", "--format", "json", "--all"}),
					reflect.DeepEqual(args, []string{"list", "--format", "json"}):
					return `[{"configuration":{"id":"demo_app_1","labels":{"` + accProjectLabel + `":"demo","` + accServiceLabel + `":"app","` + accConfigHashLabel + `":"` + configHash + `","` + accImageIDLabel + `":"` + tt.oldID + `"}}}]`, nil
				case reflect.DeepEqual(args, []string{"inspect", "demo_app_1"}):
					return `[{"configuration":{"id":"demo_app_1","mounts":[]}}]`, nil
				default:
					return "", nil
				}
			}
			networkRun := func(_ context.Context, args ...string) (string, error) {
				switch {
				case reflect.DeepEqual(args, []string{"network", "inspect", "demo_default"}):
					return `[{"configuration":{"name":"demo_default","labels":{"` + accProjectLabel + `":"demo","` + accNetworkLabel + `":"default"}}}]`, nil
				default:
					return "[]", nil
				}
			}
			client := &ComposeClient{containerClient: &container.Client{
				Container: container.NewContainerClient(run, nil),
				Images:    container.NewImageClient(run, nil),
				Volumes:   container.NewVolumeClient(run),
				Networks:  container.NewNetworkClient(networkRun),
			}}
			if err := client.Up(context.Background(), composePath, ParseOptions{}, UpOptions{}); err != nil {
				t.Fatal(err)
			}
			foundRun := false
			for _, call := range calls {
				if len(call) == 0 || call[0] != "run" {
					continue
				}
				foundRun = true
				if !hasArgument(call, accImageIDLabel+"=sha256:new") {
					t.Fatalf("new container lacks image identity label: %#v", call)
				}
			}
			if foundRun != tt.wantCreate {
				t.Fatalf("created = %t, want %t; calls = %#v", foundRun, tt.wantCreate, calls)
			}
		})
	}
}

func TestUpImageOnlyDoesNotRecreateAfterInitialRuntimePull(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `name: demo
services:
  app:
    image: demo-app:latest
`)
	localImage := false
	containerExists := false
	configHash := ""
	runCount := 0
	run := func(_ context.Context, args ...string) (string, error) {
		switch {
		case reflect.DeepEqual(args, []string{"image", "inspect", "demo-app:latest"}):
			if !localImage {
				return "image not found", errors.New("image not found")
			}
			return `{"descriptor":{"digest":"sha256:pulled"}}`, nil
		case reflect.DeepEqual(args, []string{"list", "--format", "json", "--all"}),
			reflect.DeepEqual(args, []string{"list", "--format", "json"}):
			if !containerExists {
				return "[]", nil
			}
			return `[{"configuration":{"id":"demo_app_1","labels":{"` + accProjectLabel + `":"demo","` + accServiceLabel + `":"app","` + accConfigHashLabel + `":"` + configHash + `"}}}]`, nil
		case reflect.DeepEqual(args, []string{"inspect", "demo_app_1"}):
			return `[{"configuration":{"id":"demo_app_1","image":{"reference":"demo-app:latest","descriptor":{"digest":"sha256:pulled"}},"mounts":[]}}]`, nil
		case len(args) > 0 && args[0] == "run":
			runCount++
			localImage = true
			containerExists = true
			for _, arg := range args {
				if strings.HasPrefix(arg, accConfigHashLabel+"=") {
					configHash = strings.TrimPrefix(arg, accConfigHashLabel+"=")
				}
				if strings.HasPrefix(arg, accImageIDLabel+"=") {
					t.Fatalf("first run unexpectedly knew remotely pulled image identity: %#v", args)
				}
			}
			return "", nil
		case len(args) > 0 && (args[0] == "stop" || args[0] == "delete"):
			t.Fatalf("repeated up recreated unchanged container: %#v", args)
			return "", nil
		default:
			return "", nil
		}
	}
	networkRun := func(_ context.Context, args ...string) (string, error) {
		if reflect.DeepEqual(args, []string{"network", "inspect", "demo_default"}) {
			return `[{"configuration":{"name":"demo_default","labels":{"` + accProjectLabel + `":"demo","` + accNetworkLabel + `":"default"}}}]`, nil
		}
		return "[]", nil
	}
	client := &ComposeClient{containerClient: &container.Client{
		Container: container.NewContainerClient(run, nil),
		Images:    container.NewImageClient(run, nil),
		Volumes:   container.NewVolumeClient(run),
		Networks:  container.NewNetworkClient(networkRun),
	}}
	for iteration := 0; iteration < 2; iteration++ {
		if err := client.Up(context.Background(), composePath, ParseOptions{}, UpOptions{}); err != nil {
			t.Fatalf("Up() iteration %d: %v", iteration+1, err)
		}
	}
	if runCount != 1 || configHash == "" {
		t.Fatalf("run count = %d, config hash = %q; want one initial run with hash", runCount, configHash)
	}
}

func TestUpWarnsAndRoundsFractionalCPUAllocation(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `name: demo
services:
  app:
    image: alpine
    cpus: 0.5
`)
	var warnings []string
	var runArgs []string
	run := func(_ context.Context, args ...string) (string, error) {
		if len(args) == 3 && args[0] == "network" && args[1] == "inspect" {
			return "network not found", errors.New("network not found")
		}
		if len(args) > 0 && args[0] == "run" {
			runArgs = append([]string(nil), args...)
		}
		return "", nil
	}
	client := &ComposeClient{containerClient: &container.Client{
		Container: container.NewContainerClient(run, nil),
		Images:    newTestImageClient(),
		Volumes:   container.NewVolumeClient(run),
		Networks:  container.NewNetworkClient(run),
	}}
	if err := client.Up(context.Background(), composePath, ParseOptions{}, UpOptions{OnWarning: func(message string) {
		warnings = append(warnings, message)
	}}); err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "0.5 CPUs") || !strings.Contains(warnings[0], "allocate 1 CPU") {
		t.Fatalf("warnings = %#v, want requested and rounded CPU counts", warnings)
	}
	if !hasArgument(runArgs, "1") || !hasArgument(runArgs, "--cpus") {
		t.Fatalf("run args = %#v, want --cpus 1", runArgs)
	}
}

func TestUpPreflightsLaterServiceBeforeCreatingResources(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `name: demo
services:
  first:
    image: alpine
  second:
    image: alpine
    ports:
      - "80"
`)
	var calls [][]string
	run := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		return "", nil
	}
	client := &ComposeClient{containerClient: &container.Client{
		Container: container.NewContainerClient(run, nil),
		Images:    container.NewImageClient(run, nil),
		Volumes:   container.NewVolumeClient(run),
		Networks:  container.NewNetworkClient(run),
	}}
	err := client.Up(context.Background(), composePath, ParseOptions{}, UpOptions{})
	if !errors.Is(err, ErrUnsupportedFeature) {
		t.Fatalf("Up() error = %v, want ErrUnsupportedFeature", err)
	}
	if len(calls) != 0 {
		t.Fatalf("runtime calls = %#v, want no runtime mutation or inspection before preflight", calls)
	}
}

func TestUpPreflightsMemoryMinimumBeforeCreatingResources(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `name: demo
services:
  first:
    image: alpine
  second:
    image: alpine
    mem_limit: 128m
`)
	var calls [][]string
	run := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		return "", nil
	}
	client := &ComposeClient{containerClient: &container.Client{
		Container: container.NewContainerClient(run, nil),
		Images:    container.NewImageClient(run, nil),
		Volumes:   container.NewVolumeClient(run),
		Networks:  container.NewNetworkClient(run),
	}}
	err := client.Up(context.Background(), composePath, ParseOptions{}, UpOptions{})
	if !errors.Is(err, container.ErrInvalidOptions) || !strings.Contains(err.Error(), "200 MiB") {
		t.Fatalf("Up() error = %v, want Apple Container memory minimum", err)
	}
	if len(calls) != 0 {
		t.Fatalf("runtime calls = %#v, want none before preflight", calls)
	}
}

func TestServiceConfigHashIncludesRuntimeSettings(t *testing.T) {
	base := func() container.CreateOptions {
		return container.CreateOptions{
			Name: "demo_app_1",
			Labels: map[string]string{
				accProjectLabel: "demo",
				accServiceLabel: "app",
			},
		}
	}
	baseHash, err := serviceConfigHash("alpine", base())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		image  string
		mutate func(*container.CreateOptions)
	}{
		{name: "image", image: "busybox"},
		{name: "cpus", image: "alpine", mutate: func(opts *container.CreateOptions) { opts.CPUs = 2 }},
		{name: "memory", image: "alpine", mutate: func(opts *container.CreateOptions) { opts.Memory = "512M" }},
		{name: "environment", image: "alpine", mutate: func(opts *container.CreateOptions) { opts.Environment = []string{"MODE=prod"} }},
		{name: "dns", image: "alpine", mutate: func(opts *container.CreateOptions) { opts.Nameservers = []string{"1.1.1.1"} }},
		{name: "network", image: "alpine", mutate: func(opts *container.CreateOptions) { opts.Networks = []string{"demo_backend"} }},
		{name: "mount", image: "alpine", mutate: func(opts *container.CreateOptions) {
			opts.Mounts = []container.Mount{{Type: container.MountTypeBind, Source: "/tmp/source", Target: "/data"}}
		}},
		{name: "port", image: "alpine", mutate: func(opts *container.CreateOptions) {
			opts.Publish = []container.PortMapping{{HostPort: 8080, ContainerPort: 80, Protocol: container.TCP}}
		}},
		{name: "command", image: "alpine", mutate: func(opts *container.CreateOptions) { opts.Arguments = []string{"sleep", "60"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := base()
			if tt.mutate != nil {
				tt.mutate(&opts)
			}
			got, err := serviceConfigHash(tt.image, opts)
			if err != nil {
				t.Fatal(err)
			}
			if got == baseHash {
				t.Fatalf("hash = %q, want changed runtime setting to affect it", got)
			}
		})
	}
}
