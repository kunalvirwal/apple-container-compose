package state

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStoreEnsureUpsertAndRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coredns", "state.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state file was not created: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "\n  \"version\": 1,\n") || !strings.HasSuffix(string(contents), "\n") {
		t.Fatalf("state file is not indented JSON:\n%s", contents)
	}

	container := testContainer("demo_api_1", "api", "10.0.0.3")
	container.Nameservers = []string{"1.1.1.1", "8.8.8.8"}
	if err := store.Upsert(context.Background(), container); err != nil {
		t.Fatal(err)
	}
	state, err := store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Version != stateVersion || !reflect.DeepEqual(state.Containers, []Container{container}) {
		t.Fatalf("state = %#v", state)
	}

	container.Networks["demo_default"] = NetworkAttachment{Addresses: []string{"10.0.0.4"}}
	if err := store.Upsert(context.Background(), container); err != nil {
		t.Fatal(err)
	}
	state, err = store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Containers) != 1 || state.Containers[0].Networks["demo_default"].Addresses[0] != "10.0.0.4" {
		t.Fatalf("upsert did not replace container: %#v", state)
	}

	if err := store.Remove(context.Background(), []string{"demo_api_1"}); err != nil {
		t.Fatal(err)
	}
	state, err = store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state, State{Version: stateVersion, Containers: []Container{}}) {
		t.Fatalf("state after remove = %#v", state)
	}
}

func TestStoreRejectsInvalidMutationWithoutReplacingState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	valid := testContainer("demo_api_1", "api", "10.0.0.3")
	if err := store.Upsert(context.Background(), valid); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(context.Background(), testContainer("bad", "api", "127.0.0.1")); err == nil {
		t.Fatal("invalid endpoint was accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("invalid mutation replaced state:\n%s\n%s", before, after)
	}
}

func TestStoreSerializesConcurrentUpserts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	first, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 2)
	go func() { errCh <- first.Upsert(context.Background(), testContainer("demo_api_1", "api", "10.0.0.3")) }()
	go func() {
		errCh <- second.Upsert(context.Background(), testContainer("demo_worker_1", "worker", "10.0.0.4"))
	}()
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
	state, err := first.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Containers) != 2 || state.Containers[0].ID != "demo_api_1" || state.Containers[1].ID != "demo_worker_1" {
		t.Fatalf("concurrent state = %#v", state)
	}
}

func testContainer(id, service, address string) Container {
	return Container{
		ID:      id,
		Service: service,
		Networks: map[string]NetworkAttachment{
			"demo_default": {Addresses: []string{address}},
		},
	}
}
