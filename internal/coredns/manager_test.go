package coredns

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kunalvirwal/apple-container-compose/pkg/container"
)

func TestManagerStartCreatesAndInspectsCoreDNS(t *testing.T) {
	stateDir := t.TempDir()
	statePath := filepath.Join(stateDir, "state.json")
	if err := os.WriteFile(statePath, []byte("{\"version\":1,\"containers\":[]}"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	run := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		switch {
		case reflect.DeepEqual(args, []string{"run", "--name", "demo-coredns", "--label", projectLabel + "=demo", "--label", roleLabel + "=" + coreDNSRole, "--network", "demo_backend", "--network", "demo_frontend", "--mount", "type=bind,source=" + stateDir + ",target=/config,readonly", "-d", defaultImage}):
			return "", nil
		case reflect.DeepEqual(args, []string{"inspect", "demo-coredns"}):
			return `[{"configuration":{"id":"demo-coredns","mounts":[]},"status":{"networks":[{"network":"demo_backend","ipv4Address":"192.168.64.5/24"},{"network":"demo_frontend","ipv4Address":"192.168.65.5/24"}]}}]`, nil
		default:
			return "", errors.New("unexpected command")
		}
	}

	manager, err := NewManager(&container.Client{Container: container.NewContainerClient(run, nil)}, ManagerOptions{StatePath: statePath})
	if err != nil {
		t.Fatal(err)
	}
	addresses, err := manager.Start(context.Background(), "demo", []string{"demo_frontend", "demo_backend"})
	if err != nil {
		t.Fatalf("Start() error = %v, calls = %#v", err, calls)
	}
	want := map[string]netip.Addr{
		"demo_backend":  netip.MustParseAddr("192.168.64.5"),
		"demo_frontend": netip.MustParseAddr("192.168.65.5"),
	}
	if !reflect.DeepEqual(addresses, want) {
		t.Fatalf("addresses = %#v, want %#v", addresses, want)
	}
}

func TestNetworkIPv4Addresses(t *testing.T) {
	got, err := networkIPv4Addresses([]container.NetworkAttachment{
		{
			Name:      "demo_default",
			Addresses: []netip.Addr{netip.MustParseAddr("fde2:6153::5"), netip.MustParseAddr("192.168.64.5")},
		},
		{
			Name:      "unrequested",
			Addresses: []netip.Addr{netip.MustParseAddr("192.168.65.5")},
		},
	}, []string{"demo_default"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("addresses = %#v, want only requested network", got)
	}
	if want := netip.MustParseAddr("192.168.64.5"); got["demo_default"] != want {
		t.Fatalf("address = %s, want %s", got["demo_default"], want)
	}
}
