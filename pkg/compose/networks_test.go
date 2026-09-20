package compose

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/kunalvirwal/apple-container-compose/pkg/container"
)

func TestUpCreatesDefaultNetworkAndAttachesServices(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `name: demo
services:
  api:
    image: alpine
  worker:
    image: alpine
`)

	var calls [][]string
	run := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		if reflect.DeepEqual(args, []string{"network", "inspect", "demo_default"}) {
			return "network not found", errors.New("command failed")
		}
		return "", nil
	}
	client := &ComposeClient{containerClient: &container.Client{
		Container: container.NewContainerClient(run, nil),
		Volumes:   container.NewVolumeClient(run),
		Networks:  container.NewNetworkClient(run),
	}}

	if err := client.Up(context.Background(), composePath, ParseOptions{}, UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	wantCreate := []string{
		"network", "create",
		"--label", accNetworkLabel + "=default",
		"--label", accNetworkCoreDNSLabel + "=false",
		"--label", accProjectLabel + "=demo",
		"demo_default",
	}
	if !hasCall(calls, wantCreate) {
		t.Fatalf("Up() calls = %#v, want network create %#v", calls, wantCreate)
	}

	runs := 0
	for _, call := range calls {
		if len(call) == 0 || call[0] != "run" {
			continue
		}
		runs++
		if !hasArgument(call, "--network") || !hasArgument(call, "demo_default") {
			t.Fatalf("run args = %#v, want --network demo_default", call)
		}
	}
	if runs != 2 {
		t.Fatalf("run calls = %d, want 2; all calls = %#v", runs, calls)
	}
}

func TestUpCreatesAndAttachesDeclaredNetworks(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `name: demo
services:
  app:
    image: alpine
    networks:
      - backend
      - frontend
  worker:
    image: alpine
    networks:
      - backend
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

	if err := client.Up(context.Background(), composePath, ParseOptions{}, UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	for _, want := range [][]string{
		{"network", "create", "--label", accNetworkLabel + "=backend", "--label", accNetworkCoreDNSLabel + "=false", "--label", accProjectLabel + "=demo", "demo_backend"},
		{"network", "create", "--label", accNetworkLabel + "=frontend", "--label", accNetworkCoreDNSLabel + "=false", "--label", accProjectLabel + "=demo", "demo_frontend"},
	} {
		if !hasCall(calls, want) {
			t.Fatalf("Up() calls = %#v, want network create %#v", calls, want)
		}
	}

	for _, call := range calls {
		if len(call) == 0 || call[0] != "run" {
			continue
		}
		if hasArgument(call, "demo_app_1") && (!hasArgument(call, "demo_backend") || !hasArgument(call, "demo_frontend")) {
			t.Fatalf("app run args = %#v, want backend and frontend", call)
		}
		if hasArgument(call, "demo_worker_1") && (!hasArgument(call, "demo_backend") || hasArgument(call, "demo_frontend")) {
			t.Fatalf("worker run args = %#v, want only backend", call)
		}
	}
}

func TestUpDoesNotAttachExplicitlyNetworkedServiceToDefaultNetwork(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `name: demo
services:
  api:
    image: alpine
    networks:
      - backend
  worker:
    image: alpine
networks:
  backend:
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

	if err := client.Up(context.Background(), composePath, ParseOptions{}, UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	for _, call := range calls {
		if len(call) == 0 || call[0] != "run" {
			continue
		}
		switch {
		case hasArgument(call, "demo_api_1"):
			if !hasArgument(call, "demo_backend") || hasArgument(call, "demo_default") {
				t.Fatalf("api run args = %#v, want only demo_backend", call)
			}
		case hasArgument(call, "demo_worker_1"):
			if !hasArgument(call, "demo_default") || hasArgument(call, "demo_backend") {
				t.Fatalf("worker run args = %#v, want only demo_default", call)
			}
		}
	}
}

func TestUpSkipsDefaultNetworkWhenEveryServiceDeclaresNetworks(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `name: demo
services:
  api:
    image: alpine
    networks:
      - backend
  worker:
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

	if err := client.Up(context.Background(), composePath, ParseOptions{}, UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	for _, call := range calls {
		if hasArgument(call, "demo_default") {
			t.Fatalf("runtime calls = %#v, must not create, inspect, or attach demo_default", calls)
		}
	}
}

func TestUpRejectsNetworkOwnedByAnotherProject(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `name: demo
services:
  app:
    image: alpine
`)

	var calls [][]string
	run := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		if reflect.DeepEqual(args, []string{"network", "inspect", "demo_default"}) {
			return `[{"configuration":{"name":"demo_default","labels":{"` + accProjectLabel + `":"other"}}}]`, nil
		}
		t.Fatalf("unexpected runtime call: %#v", args)
		return "", nil
	}
	client := &ComposeClient{containerClient: &container.Client{
		Container: container.NewContainerClient(run, nil),
		Volumes:   container.NewVolumeClient(run),
		Networks:  container.NewNetworkClient(run),
	}}

	var warnings []string
	err := client.Up(context.Background(), composePath, ParseOptions{}, UpOptions{
		OnFatalWarning: func(message string) { warnings = append(warnings, message) },
	})
	if !errors.Is(err, ErrUnsupportedFeature) {
		t.Fatalf("Up() error = %v, want ErrUnsupportedFeature", err)
	}
	want := `network "demo_default" already exists but is not owned by ACC project "demo"; choose a different project name`
	if len(warnings) != 1 || warnings[0] != want {
		t.Fatalf("warnings = %#v, want %#v", warnings, []string{want})
	}
	if len(calls) != 1 || !reflect.DeepEqual(calls[0], []string{"network", "inspect", "demo_default"}) {
		t.Fatalf("calls = %#v, want only network inspect", calls)
	}
}

func TestDownRemovesOnlyOwnedProjectNetworks(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `name: demo
services:
  app:
    image: alpine
`)

	var calls [][]string
	listCalls := 0
	run := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		switch {
		case reflect.DeepEqual(args, []string{"list", "--format", "json", "--all"}):
			listCalls++
			if listCalls > 1 {
				return "[]", nil
			}
			return `[{"configuration":{"id":"demo_app_1","labels":{"` + accProjectLabel + `":"demo","` + accServiceLabel + `":"app"}}}]`, nil
		case reflect.DeepEqual(args, []string{"network", "list", "--format", "json"}):
			return `[
  {"configuration":{"name":"demo_frontend","labels":{"` + accProjectLabel + `":"demo","` + accNetworkLabel + `":"frontend"}}},
  {"configuration":{"name":"demo_backend","labels":{"` + accProjectLabel + `":"demo","` + accNetworkLabel + `":"backend"}}}
]`, nil
		default:
			return "", nil
		}
	}
	client := &ComposeClient{containerClient: &container.Client{
		Container: container.NewContainerClient(run, nil),
		Volumes:   container.NewVolumeClient(run),
		Networks:  container.NewNetworkClient(run),
	}}

	if err := client.Down(context.Background(), composePath, ParseOptions{}, DownOptions{}); err != nil {
		t.Fatalf("Down() error = %v", err)
	}
	if !hasCall(calls, []string{"network", "delete", "demo_backend", "demo_frontend"}) {
		t.Fatalf("Down() calls = %#v, want sorted owned-network delete", calls)
	}
}

func TestDownRetainsForeignDefaultNetwork(t *testing.T) {
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
			return "[]", nil
		case reflect.DeepEqual(args, []string{"network", "list", "--format", "json"}):
			return `[{"configuration":{"name":"demo_default","labels":{"` + accProjectLabel + `":"other","` + accNetworkLabel + `":"default"}}}]`, nil
		default:
			t.Fatalf("unexpected runtime call: %#v", args)
			return "", nil
		}
	}
	client := &ComposeClient{containerClient: &container.Client{
		Container: container.NewContainerClient(run, nil),
		Volumes:   container.NewVolumeClient(run),
		Networks:  container.NewNetworkClient(run),
	}}

	if err := client.Down(context.Background(), composePath, ParseOptions{}, DownOptions{}); err != nil {
		t.Fatalf("Down() error = %v", err)
	}
	for _, call := range calls {
		if len(call) >= 2 && call[0] == "network" && call[1] == "delete" {
			t.Fatalf("Down() calls = %#v, must retain a foreign network", calls)
		}
	}
}
