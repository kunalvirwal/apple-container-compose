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

func TestManagerRemoveIfUnusedRetainsCoreDNSForRemainingProjectContainer(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	var calls [][]string
	run := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		if reflect.DeepEqual(args, []string{"list", "--format", "json", "--all"}) {
			return `[
  {"configuration":{"id":"demo_old_1","labels":{"` + projectLabel + `":"demo","io.github.kunalvirwal.acc.service":"old"}}},
  {"configuration":{"id":"demo-coredns","labels":{"` + projectLabel + `":"demo","` + roleLabel + `":"` + coreDNSRole + `"}}}
]`, nil
		}
		t.Fatalf("unexpected command: %#v", args)
		return "", nil
	}
	manager, err := NewManager(&container.Client{Container: container.NewContainerClient(run, nil)}, ManagerOptions{StatePath: statePath})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RemoveIfUnused(context.Background(), "demo", false); err != nil {
		t.Fatalf("RemoveIfUnused() error = %v", err)
	}
	want := [][]string{{"list", "--format", "json", "--all"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestManagerRemoveIfUnusedDeletesAllCoreDNSContainers(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	var calls [][]string
	run := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		switch {
		case reflect.DeepEqual(args, []string{"list", "--format", "json", "--all"}):
			return `[
  {"configuration":{"id":"demo-coredns-b","labels":{"` + projectLabel + `":"demo","` + roleLabel + `":"` + coreDNSRole + `"}}},
  {"configuration":{"id":"demo-coredns-a","labels":{"` + projectLabel + `":"demo","` + roleLabel + `":"` + coreDNSRole + `"}}}
]`, nil
		case reflect.DeepEqual(args, []string{"stop", "demo-coredns-a", "demo-coredns-b"}),
			reflect.DeepEqual(args, []string{"delete", "demo-coredns-a", "demo-coredns-b"}):
			return "", nil
		default:
			t.Fatalf("unexpected command: %#v", args)
			return "", nil
		}
	}
	manager, err := NewManager(&container.Client{Container: container.NewContainerClient(run, nil)}, ManagerOptions{StatePath: statePath})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RemoveIfUnused(context.Background(), "demo", false); err != nil {
		t.Fatalf("RemoveIfUnused() error = %v", err)
	}
	want := [][]string{
		{"list", "--format", "json", "--all"},
		{"stop", "demo-coredns-a", "demo-coredns-b"},
		{"delete", "demo-coredns-a", "demo-coredns-b"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}
