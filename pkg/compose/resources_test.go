package compose

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/kunalvirwal/apple-container-compose/pkg/container"
)

func TestUpResourceLimitSyntax(t *testing.T) {
	tests := []struct {
		name     string
		settings string
		cpus     string
		memory   string
		wantWarn bool
	}{
		{"service limits", "    cpus: 2\n    mem_limit: 512M\n", "2", "512M", false},
		{"deploy limits", "    deploy:\n      resources:\n        limits:\n          cpus: \"2\"\n          memory: 512M\n", "2", "512M", false},
		{"identical declarations", "    cpus: 2\n    mem_limit: 512M\n    deploy:\n      resources:\n        limits:\n          cpus: \"2\"\n          memory: \"536870912\"\n", "2", "512M", false},
		{"deploy CPU only", "    deploy:\n      resources:\n        limits:\n          cpus: \"2\"\n", "2", "", false},
		{"deploy memory only", "    deploy:\n      resources:\n        limits:\n          memory: 512M\n", "", "512M", false},
		{"fractional deploy CPU", "    deploy:\n      resources:\n        limits:\n          cpus: \"0.5\"\n", "1", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, path := writeVolumeCompose(t, "name: demo\nservices:\n  app:\n    image: alpine:3.20\n    command: [sleep, '3600']\n"+tt.settings)
			var runArgs, warnings []string
			run := func(_ context.Context, args ...string) (string, error) {
				if len(args) > 1 && args[0] == "network" && args[1] == "inspect" {
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
			if err := client.Up(context.Background(), path, ParseOptions{}, UpOptions{OnWarning: func(message string) {
				warnings = append(warnings, message)
			}}); err != nil {
				t.Fatal(err)
			}
			if len(runArgs) == 0 {
				t.Fatal("no service run command")
			}
			for flag, want := range map[string]string{"--cpus": tt.cpus, "--memory": tt.memory} {
				got := ""
				for i := 0; i+1 < len(runArgs); i++ {
					if runArgs[i] == flag {
						got = runArgs[i+1]
					}
				}
				if got != want {
					t.Fatalf("%s = %q, want %q; args = %#v", flag, got, want, runArgs)
				}
			}
			if tt.wantWarn {
				if len(warnings) != 1 || !strings.Contains(warnings[0], "0.5 CPUs") || !strings.Contains(warnings[0], "allocate 1 CPU") {
					t.Fatalf("warnings = %#v, want fractional CPU allocation warning", warnings)
				}
			} else if len(warnings) != 0 {
				t.Fatalf("unexpected warnings: %#v", warnings)
			}
		})
	}
}

func TestUpRejectsInvalidDeployLimitsBeforeRuntimeCalls(t *testing.T) {
	tests := []struct {
		name     string
		settings string
		message  string
	}{
		{"CPU conflict", "    cpus: 1\n    deploy:\n      resources:\n        limits:\n          cpus: \"2\"\n", "can't set distinct values on 'cpus' and 'deploy.resources.limits.cpus'"},
		{"memory conflict", "    mem_limit: 256M\n    deploy:\n      resources:\n        limits:\n          memory: 512M\n", "can't set distinct values on 'mem_limit' and 'deploy.resources.limits.memory'"},
		{"memory minimum", "    deploy:\n      resources:\n        limits:\n          memory: 128M\n", "200 MiB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, path := writeVolumeCompose(t, "name: demo\nservices:\n  first:\n    image: alpine\n  second:\n    image: alpine\n"+tt.settings)
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
			err := client.Up(context.Background(), path, ParseOptions{}, UpOptions{})
			if err == nil || !strings.Contains(err.Error(), tt.message) {
				t.Fatalf("Up() = %v, want %q", err, tt.message)
			}
			if tt.name == "memory minimum" && !errors.Is(err, container.ErrInvalidOptions) {
				t.Fatalf("Up() = %v, want ErrInvalidOptions", err)
			}
			if len(calls) != 0 {
				t.Fatalf("runtime calls = %#v, want none before validation", calls)
			}
		})
	}
}

func TestUpReconcilesDeployLimitsByEffectiveAllocation(t *testing.T) {
	// The old container was created with service-level cpus/mem_limit.
	oldHash, err := serviceConfigHash("alpine", container.CreateOptions{
		Name: "demo_app_1", CPUs: 2, Memory: "512M",
		Labels:   map[string]string{accProjectLabel: "demo", accServiceLabel: "app"},
		Networks: []string{"demo_default"},
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		cpu      string
		memory   string
		recreate bool
	}{
		{"equivalent syntax reuses", "2", "512M", false},
		{"CPU change recreates", "3", "512M", true},
		{"memory change recreates", "2", "1G", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, path := writeVolumeCompose(t, "name: demo\nservices:\n  app:\n    image: alpine\n    deploy:\n      resources:\n        limits:\n          cpus: \""+tt.cpu+"\"\n          memory: "+tt.memory+"\n")
			var mutations [][]string
			run := func(_ context.Context, args ...string) (string, error) {
				switch {
				case reflect.DeepEqual(args, []string{"list", "--format", "json", "--all"}):
					return `[{"configuration":{"id":"demo_app_1","labels":{"` + accProjectLabel + `":"demo","` + accServiceLabel + `":"app","` + accConfigHashLabel + `":"` + oldHash + `"}}}]`, nil
				case reflect.DeepEqual(args, []string{"list", "--format", "json"}):
					return `[{"configuration":{"id":"demo_app_1"}}]`, nil
				case reflect.DeepEqual(args, []string{"inspect", "demo_app_1"}):
					return `[{"configuration":{"id":"demo_app_1","mounts":[]}}]`, nil
				case len(args) > 0 && (args[0] == "run" || args[0] == "stop" || args[0] == "delete" || args[0] == "start"):
					mutations = append(mutations, append([]string(nil), args...))
				}
				return "", nil
			}
			networkRun := func(_ context.Context, args ...string) (string, error) {
				if reflect.DeepEqual(args, []string{"network", "inspect", "demo_default"}) {
					return `[{"configuration":{"name":"demo_default","labels":{"` + accProjectLabel + `":"demo","` + accNetworkLabel + `":"default"}}}]`, nil
				}
				return "[]", nil
			}
			client := &ComposeClient{containerClient: &container.Client{
				Container: container.NewContainerClient(run, nil),
				Images:    newTestImageClient(),
				Volumes:   container.NewVolumeClient(run),
				Networks:  container.NewNetworkClient(networkRun),
			}}
			if err := client.Up(context.Background(), path, ParseOptions{}, UpOptions{}); err != nil {
				t.Fatal(err)
			}
			if !tt.recreate {
				if len(mutations) != 0 {
					t.Fatalf("equivalent allocation mutated container: %#v", mutations)
				}
				return
			}
			if len(mutations) != 3 || !reflect.DeepEqual(mutations[0], []string{"stop", "demo_app_1"}) || !reflect.DeepEqual(mutations[1], []string{"delete", "demo_app_1"}) || mutations[2][0] != "run" {
				t.Fatalf("mutations = %#v, want stop, delete, run", mutations)
			}
		})
	}
}
