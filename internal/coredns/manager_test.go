package coredns

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
		case reflect.DeepEqual(args, []string{"list", "--format", "json", "--all"}):
			return "[]", nil
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

func TestManagerStartReusesExistingCoreDNS(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(statePath, []byte("{\"version\":1,\"containers\":[]}"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	run := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		switch {
		case reflect.DeepEqual(args, []string{"list", "--format", "json", "--all"}):
			return `[{"configuration":{"id":"demo-coredns","labels":{"` + projectLabel + `":"demo","` + roleLabel + `":"` + coreDNSRole + `"}}}]`, nil
		case reflect.DeepEqual(args, []string{"list", "--format", "json"}):
			return `[{"configuration":{"id":"demo-coredns","labels":{"` + projectLabel + `":"demo","` + roleLabel + `":"` + coreDNSRole + `"}}}]`, nil
		case reflect.DeepEqual(args, []string{"inspect", "demo-coredns"}):
			return `[{"configuration":{"id":"demo-coredns","mounts":[]},"status":{"networks":[{"network":"demo_backend","ipv4Address":"192.168.64.5/24"},{"network":"demo_frontend","ipv4Address":"192.168.65.5/24"}]}}]`, nil
		default:
			t.Fatalf("unexpected command: %#v", args)
			return "", nil
		}
	}

	manager, err := NewManager(&container.Client{Container: container.NewContainerClient(run, nil)}, ManagerOptions{StatePath: statePath})
	if err != nil {
		t.Fatal(err)
	}
	addresses, err := manager.Start(context.Background(), "demo", []string{"demo_frontend", "demo_backend"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	wantAddresses := map[string]netip.Addr{
		"demo_backend":  netip.MustParseAddr("192.168.64.5"),
		"demo_frontend": netip.MustParseAddr("192.168.65.5"),
	}
	if !reflect.DeepEqual(addresses, wantAddresses) {
		t.Fatalf("addresses = %#v, want %#v", addresses, wantAddresses)
	}
	wantCalls := [][]string{
		{"list", "--format", "json", "--all"},
		{"inspect", "demo-coredns"},
		{"list", "--format", "json"},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestManagerStartMigratesChangedNetworksWithoutStoppingOldDNS(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(statePath, []byte("{\"version\":1,\"containers\":[]}"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	newName := ""
	const newID = "opaque-new-coredns-id"
	run := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		switch {
		case reflect.DeepEqual(args, []string{"list", "--format", "json", "--all"}):
			result := `[{"configuration":{"id":"demo-coredns","labels":{"` + projectLabel + `":"demo","` + roleLabel + `":"` + coreDNSRole + `"}}}`
			if newName != "" {
				result += `,{"configuration":{"id":"` + newID + `","labels":{"` + projectLabel + `":"demo","` + roleLabel + `":"` + coreDNSRole + `"}}}`
			}
			return result + `]`, nil
		case reflect.DeepEqual(args, []string{"inspect", "demo-coredns"}):
			return `[{"configuration":{"id":"demo-coredns","mounts":[]},"status":{"networks":[{"network":"demo_backend","ipv4Address":"192.168.64.5/24"}]}}]`, nil
		case newName != "" && reflect.DeepEqual(args, []string{"inspect", newName}):
			return `[{"configuration":{"id":"` + newID + `","mounts":[]},"status":{"networks":[{"network":"demo_backend","ipv4Address":"192.168.64.6/24"},{"network":"demo_frontend","ipv4Address":"192.168.65.6/24"}]}}]`, nil
		case len(args) > 3 && args[0] == "run" && args[1] == "--name" && strings.HasPrefix(args[2], "demo-coredns-"):
			newName = args[2]
			return "", nil
		case reflect.DeepEqual(args, []string{"list", "--format", "json"}):
			return `[{"configuration":{"id":"demo-coredns"}},{"configuration":{"id":"` + newID + `"}}]`, nil
		case reflect.DeepEqual(args, []string{"stop", "demo-coredns"}), reflect.DeepEqual(args, []string{"delete", "demo-coredns"}):
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
	addresses, err := manager.Start(context.Background(), "demo", []string{"demo_backend", "demo_frontend"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if len(addresses) != 2 {
		t.Fatalf("addresses = %#v, want two networks", addresses)
	}
	if newName == "" {
		t.Fatalf("calls = %#v, want a new CoreDNS generation", calls)
	}
	for _, call := range calls {
		if len(call) > 0 && (call[0] == "stop" || call[0] == "delete") {
			t.Fatalf("Start stopped old DNS before service migration: %#v", calls)
		}
	}
	retired, err := manager.FinalizeMigration(context.Background(), "demo", []string{"app"})
	if err != nil || !retired {
		t.Fatalf("FinalizeMigration() = %t, %v; want retired older DNS", retired, err)
	}
	if !hasCoreDNSCall(calls, []string{"stop", "demo-coredns"}) || !hasCoreDNSCall(calls, []string{"delete", "demo-coredns"}) {
		t.Fatalf("calls = %#v, want old DNS removed after migration", calls)
	}
}

func TestManagerStartReusesMatchingGenerationAfterInterruptedMigration(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(statePath, []byte("{\"version\":1,\"containers\":[]}"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(_ context.Context, args ...string) (string, error) {
		switch {
		case reflect.DeepEqual(args, []string{"list", "--format", "json", "--all"}):
			return `[
  {"configuration":{"id":"demo-coredns-a","labels":{"` + projectLabel + `":"demo","` + roleLabel + `":"` + coreDNSRole + `"}}},
  {"configuration":{"id":"demo-coredns-b","labels":{"` + projectLabel + `":"demo","` + roleLabel + `":"` + coreDNSRole + `"}}}
]`, nil
		case reflect.DeepEqual(args, []string{"inspect", "demo-coredns-a"}):
			return `[{"configuration":{"id":"demo-coredns-a"},"status":{"networks":[{"network":"demo_old","ipv4Address":"192.168.64.5/24"}]}}]`, nil
		case reflect.DeepEqual(args, []string{"inspect", "demo-coredns-b"}):
			return `[{"configuration":{"id":"demo-coredns-b"},"status":{"networks":[{"network":"demo_default","ipv4Address":"192.168.65.5/24"}]}}]`, nil
		case reflect.DeepEqual(args, []string{"list", "--format", "json"}):
			return `[{"configuration":{"id":"demo-coredns-a"}},{"configuration":{"id":"demo-coredns-b"}}]`, nil
		}
		t.Fatalf("unexpected command: %#v", args)
		return "", nil
	}

	manager, err := NewManager(&container.Client{Container: container.NewContainerClient(run, nil)}, ManagerOptions{StatePath: statePath})
	if err != nil {
		t.Fatal(err)
	}
	addresses, err := manager.Start(context.Background(), "demo", []string{"demo_default"})
	if err != nil {
		t.Fatal(err)
	}
	if addresses["demo_default"] != netip.MustParseAddr("192.168.65.5") {
		t.Fatalf("addresses = %#v, want matching generation", addresses)
	}
}

func hasCoreDNSCall(calls [][]string, want []string) bool {
	for _, call := range calls {
		if reflect.DeepEqual(call, want) {
			return true
		}
	}
	return false
}

func TestManagerFinalizeMigrationRetainsOldDNSForOrphan(t *testing.T) {
	var calls [][]string
	run := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		if reflect.DeepEqual(args, []string{"list", "--format", "json", "--all"}) {
			return `[
  {"configuration":{"id":"demo_old_1","labels":{"` + projectLabel + `":"demo","` + serviceLabel + `":"old"}}},
  {"configuration":{"id":"demo-coredns","labels":{"` + projectLabel + `":"demo","` + roleLabel + `":"` + coreDNSRole + `"}}},
  {"configuration":{"id":"demo-coredns-new","labels":{"` + projectLabel + `":"demo","` + roleLabel + `":"` + coreDNSRole + `"}}}
]`, nil
		}
		t.Fatalf("unexpected mutation during retained-orphan migration: %#v", args)
		return "", nil
	}
	manager, err := NewManager(&container.Client{Container: container.NewContainerClient(run, nil)}, ManagerOptions{StatePath: filepath.Join(t.TempDir(), "state.json")})
	if err != nil {
		t.Fatal(err)
	}
	manager.activeProject, manager.activeID = "demo", "demo-coredns-new"
	retired, err := manager.FinalizeMigration(context.Background(), "demo", []string{"app"})
	if err != nil || retired {
		t.Fatalf("FinalizeMigration() = %t, %v; want old DNS retained", retired, err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %#v, want only project container inspection", calls)
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

func TestManagerRemoveAfterDirectDNS(t *testing.T) {
	tests := []struct {
		name       string
		containers string
		running    bool
		removed    bool
		wantDelete bool
	}{
		{
			name:       "all services reconciled",
			containers: `[{"configuration":{"id":"demo_app_1","labels":{"` + projectLabel + `":"demo","` + serviceLabel + `":"app"}}},{"configuration":{"id":"demo-coredns","labels":{"` + projectLabel + `":"demo","` + roleLabel + `":"` + coreDNSRole + `"}}}]`,
			running:    true,
			removed:    true,
			wantDelete: true,
		},
		{
			name:       "stopped CoreDNS",
			containers: `[{"configuration":{"id":"demo_app_1","labels":{"` + projectLabel + `":"demo","` + serviceLabel + `":"app"}}},{"configuration":{"id":"demo-coredns","labels":{"` + projectLabel + `":"demo","` + roleLabel + `":"` + coreDNSRole + `"}}}]`,
			removed:    true,
			wantDelete: true,
		},
		{
			name:       "orphan retained",
			containers: `[{"configuration":{"id":"demo_old_1","labels":{"` + projectLabel + `":"demo","` + serviceLabel + `":"old"}}},{"configuration":{"id":"demo-coredns","labels":{"` + projectLabel + `":"demo","` + roleLabel + `":"` + coreDNSRole + `"}}}]`,
			removed:    false,
		},
		{
			name:       "already absent",
			containers: `[{"configuration":{"id":"demo_old_1","labels":{"` + projectLabel + `":"demo","` + serviceLabel + `":"old"}}}]`,
			removed:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls [][]string
			run := func(_ context.Context, args ...string) (string, error) {
				calls = append(calls, append([]string(nil), args...))
				switch {
				case reflect.DeepEqual(args, []string{"list", "--format", "json", "--all"}):
					return tt.containers, nil
				case reflect.DeepEqual(args, []string{"list", "--format", "json"}):
					if tt.running {
						return `[{"configuration":{"id":"demo-coredns"}}]`, nil
					}
					return "[]", nil
				case reflect.DeepEqual(args, []string{"stop", "demo-coredns"}), reflect.DeepEqual(args, []string{"delete", "demo-coredns"}):
					return "", nil
				default:
					t.Fatalf("unexpected command: %#v", args)
					return "", nil
				}
			}
			manager, err := NewManager(&container.Client{Container: container.NewContainerClient(run, nil)}, ManagerOptions{StatePath: filepath.Join(t.TempDir(), "state.json")})
			if err != nil {
				t.Fatal(err)
			}
			removed, err := manager.RemoveAfterDirectDNS(context.Background(), "demo", []string{"app"})
			if err != nil {
				t.Fatal(err)
			}
			if removed != tt.removed {
				t.Fatalf("removed = %t, want %t", removed, tt.removed)
			}
			deleted := false
			stopped := false
			for _, call := range calls {
				if reflect.DeepEqual(call, []string{"delete", "demo-coredns"}) {
					deleted = true
				}
				if reflect.DeepEqual(call, []string{"stop", "demo-coredns"}) {
					stopped = true
				}
			}
			if deleted != tt.wantDelete {
				t.Fatalf("deleted = %t, want %t; calls = %#v", deleted, tt.wantDelete, calls)
			}
			if stopped != tt.running {
				t.Fatalf("stopped = %t, want %t; calls = %#v", stopped, tt.running, calls)
			}
		})
	}
}
