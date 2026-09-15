package compose

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"

	"github.com/kunalvirwal/apple-container-compose/pkg/container"
)

func TestWithServiceRegistry(t *testing.T) {
	registry := &recordingServiceRegistry{}
	client := &ComposeClient{}
	if err := WithServiceRegistry(registry)(client); err != nil {
		t.Fatalf("WithServiceRegistry() error = %v", err)
	}
	if client.registry != registry {
		t.Fatal("WithServiceRegistry() did not configure registry")
	}
	if err := WithServiceRegistry(nil)(client); !errors.Is(err, ErrServiceRegistryNil) {
		t.Fatalf("WithServiceRegistry(nil) error = %v, want ErrServiceRegistryNil", err)
	}
}

func TestUpRegistersServiceRuntimeFromInspect(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `name: demo
services:
  api:
    image: alpine
    dns:
      - 1.1.1.1
      - 8.8.8.8
    networks:
      default:
        aliases:
          - backend-api
`)
	registry := &recordingServiceRegistry{upsertErr: errors.New("registry unavailable")}
	var warnings []string
	run := func(_ context.Context, args ...string) (string, error) {
		switch {
		case reflect.DeepEqual(args, []string{"run", "--name", "demo_api_1", "--dns", "1.1.1.1", "--dns", "8.8.8.8", "--label", accProjectLabel + "=demo", "--label", accServiceLabel + "=api", "--network", "demo_default", "-d", "alpine"}):
			return "demo_api_1", nil
		case reflect.DeepEqual(args, []string{"inspect", "demo_api_1"}):
			return `[{"configuration":{"id":"demo_api_1","mounts":[]},"status":{"networks":[{"network":"demo_default","ipv4Address":"192.168.64.3/24","ipv6Address":"fde2:6153::3/64"}]}}]`, nil
		default:
			t.Fatalf("unexpected container command: %#v", args)
			return "", nil
		}
	}
	networkRun := func(_ context.Context, args ...string) (string, error) {
		if reflect.DeepEqual(args, []string{"network", "inspect", "demo_default"}) {
			return "network not found", errors.New("command failed")
		}
		if reflect.DeepEqual(args, []string{"network", "create", "--label", accNetworkLabel + "=default", "--label", accNetworkCoreDNSLabel + "=false", "--label", accProjectLabel + "=demo", "demo_default"}) {
			return "demo_default", nil
		}
		t.Fatalf("unexpected network command: %#v", args)
		return "", nil
	}
	client := &ComposeClient{containerClient: &container.Client{
		Container: container.NewContainerClient(run, nil),
		Volumes:   container.NewVolumeClient(run),
		Networks:  container.NewNetworkClient(networkRun),
	}, registry: registry}

	if err := client.Up(context.Background(), composePath, ParseOptions{}, UpOptions{
		OnWarning: func(message string) { warnings = append(warnings, message) },
	}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if registry.ensures != 1 || len(registry.upserts) != 1 {
		t.Fatalf("registry calls = ensures:%d upserts:%#v", registry.ensures, registry.upserts)
	}
	want := ServiceRuntime{
		ProjectName: "demo",
		ServiceName: "api",
		ContainerID: "demo_api_1",
		Nameservers: []string{"1.1.1.1", "8.8.8.8"},
		Networks: []ServiceNetworkRuntime{
			{
				Name: "demo_default",
				Addresses: []netip.Addr{
					netip.MustParseAddr("192.168.64.3"),
					netip.MustParseAddr("fde2:6153::3"),
				},
				Aliases: []string{"backend-api"},
			},
		},
	}
	if !reflect.DeepEqual(registry.upserts[0], want) {
		t.Fatalf("service runtime = %#v, want %#v", registry.upserts[0], want)
	}
	if wantWarnings := []string{`update service registry for service "api": registry unavailable`}; !reflect.DeepEqual(warnings, wantWarnings) {
		t.Fatalf("warnings = %#v, want %#v", warnings, wantWarnings)
	}
}

func TestDownRemovesDeletedServiceFromRegistry(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `name: demo
services:
  app:
    image: alpine
`)

	registry := &recordingServiceRegistry{removeErr: errors.New("registry unavailable")}
	var warnings []string
	run := func(_ context.Context, args ...string) (string, error) {
		switch {
		case reflect.DeepEqual(args, []string{"list", "--format", "json", "--all"}):
			return `[{"configuration":{"id":"demo_app_1","labels":{"` + accProjectLabel + `":"demo","` + accServiceLabel + `":"app"}}}]`, nil
		case reflect.DeepEqual(args, []string{"stop", "demo_app_1"}):
			return "demo_app_1", nil
		case reflect.DeepEqual(args, []string{"delete", "demo_app_1"}):
			return "demo_app_1", nil
		case reflect.DeepEqual(args, []string{"network", "list", "--format", "json"}):
			return "[]", nil
		default:
			t.Fatalf("unexpected container command: %#v", args)
			return "", nil
		}
	}
	client := &ComposeClient{containerClient: &container.Client{
		Container: container.NewContainerClient(run, nil),
		Volumes:   container.NewVolumeClient(run),
		Networks:  container.NewNetworkClient(run),
	}, registry: registry}

	if err := client.Down(context.Background(), composePath, ParseOptions{}, DownOptions{
		OnWarning: func(message string) { warnings = append(warnings, message) },
	}); err != nil {
		t.Fatalf("Down() error = %v", err)
	}
	if got, want := registry.removes, []string{"demo_app_1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("removed registry IDs = %#v, want %#v", got, want)
	}
	if wantWarnings := []string{"remove service registry records: registry unavailable"}; !reflect.DeepEqual(warnings, wantWarnings) {
		t.Fatalf("warnings = %#v, want %#v", warnings, wantWarnings)
	}
}

type recordingServiceRegistry struct {
	ensures   int
	upserts   []ServiceRuntime
	removes   []string
	upsertErr error
	removeErr error
}

func (s *recordingServiceRegistry) Ensure(context.Context) error {
	s.ensures++
	return nil
}

func (s *recordingServiceRegistry) Upsert(_ context.Context, runtime ServiceRuntime) error {
	s.upserts = append(s.upserts, runtime)
	return s.upsertErr
}

func (s *recordingServiceRegistry) Remove(_ context.Context, ids []string) error {
	s.removes = append(s.removes, ids...)
	return s.removeErr
}
