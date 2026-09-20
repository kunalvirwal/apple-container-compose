package compose

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"

	"github.com/kunalvirwal/apple-container-compose/pkg/container"
)

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
