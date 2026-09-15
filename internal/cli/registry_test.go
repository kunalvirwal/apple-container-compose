package cli

import (
	"context"
	"net/netip"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kunalvirwal/apple-container-compose/internal/state"
	"github.com/kunalvirwal/apple-container-compose/pkg/compose"
)

func TestStateServiceRegistryConvertsRuntimeRecord(t *testing.T) {
	store, err := state.NewStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	registry := &stateServiceRegistry{store: store}
	ctx := context.Background()
	if err := registry.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	runtime := compose.ServiceRuntime{
		ProjectName: "demo",
		ServiceName: "api",
		ContainerID: "demo_api_1",
		Nameservers: []string{"1.1.1.1"},
		Networks: []compose.ServiceNetworkRuntime{
			{
				Name:      "demo_default",
				Addresses: []netip.Addr{netip.MustParseAddr("192.168.64.3")},
				Aliases:   []string{"backend-api"},
			},
		},
	}
	if err := registry.Upsert(ctx, runtime); err != nil {
		t.Fatal(err)
	}

	got, err := store.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := state.State{
		Version: 1,
		Containers: []state.Container{
			{
				ID:          "demo_api_1",
				Service:     "api",
				Nameservers: []string{"1.1.1.1"},
				Networks: map[string]state.NetworkAttachment{
					"demo_default": {
						Addresses: []string{"192.168.64.3"},
						Aliases:   []string{"backend-api"},
					},
				},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("state = %#v, want %#v", got, want)
	}
	if err := registry.Remove(ctx, []string{"demo_api_1"}); err != nil {
		t.Fatal(err)
	}
	got, err = store.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Containers) != 0 {
		t.Fatalf("state after remove = %#v", got)
	}
}
