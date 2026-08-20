package compose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"

	"github.com/kunalvirwal/apple-container-compose/pkg/container"
)

func TestUpVolumeMountTypes(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		assert   func(t *testing.T, projectDir string, calls [][]string)
	}{
		{
			name: "bind",
			contents: `services:
  app:
    image: alpine
    volumes:
      - type: bind
        source: ./host-data
        target: /data
        read_only: true
`,
			assert: func(t *testing.T, projectDir string, calls [][]string) {
				t.Helper()
				source := filepath.Join(projectDir, "host-data")
				if info, err := os.Stat(source); err != nil || !info.IsDir() {
					t.Fatalf("bind source = %v, err = %v; want created directory", info, err)
				}
				if len(calls) != 1 {
					t.Fatalf("calls = %#v, want one container run", calls)
				}
				wantMount := "type=bind,source=" + source + ",target=/data,readonly"
				if !hasArgument(calls[0], wantMount) {
					t.Fatalf("run args = %#v, want mount %q", calls[0], wantMount)
				}
			},
		},
		{
			name: "named project scoped",
			contents: `name: demo
services:
  app:
    image: alpine
    volumes:
      - pgdata:/var/lib/data
volumes:
  pgdata:
`,
			assert: func(t *testing.T, _ string, calls [][]string) {
				t.Helper()
				if len(calls) != 3 {
					t.Fatalf("calls = %#v, want inspect, create, run", calls)
				}
				if !reflect.DeepEqual(calls[0], []string{"volume", "inspect", "demo_pgdata"}) {
					t.Fatalf("inspect args = %#v", calls[0])
				}
				wantCreate := []string{
					"volume", "create",
					"--label", accProjectLabel + "=demo",
					"--label", accVolumeLabel + "=pgdata",
					"demo_pgdata",
				}
				if !reflect.DeepEqual(calls[1], wantCreate) {
					t.Fatalf("create args = %#v, want %#v", calls[1], wantCreate)
				}
				if !hasArgument(calls[2], "type=volume,source=demo_pgdata,target=/var/lib/data") {
					t.Fatalf("run args = %#v", calls[2])
				}
			},
		},
		{
			name: "named explicit name",
			contents: `name: demo
services:
  app:
    image: alpine
    volumes:
      - pgdata:/var/lib/data
volumes:
  pgdata:
    name: production-db
`,
			assert: func(t *testing.T, _ string, calls [][]string) {
				t.Helper()
				if len(calls) != 3 {
					t.Fatalf("calls = %#v, want inspect, create, run", calls)
				}
				if !reflect.DeepEqual(calls[0], []string{"volume", "inspect", "production-db"}) {
					t.Fatalf("inspect args = %#v", calls[0])
				}
				if !hasArgument(calls[1], "production-db") || !hasArgument(calls[2], "type=volume,source=production-db,target=/var/lib/data") {
					t.Fatalf("calls = %#v", calls)
				}
			},
		},
		{
			name: "anonymous",
			contents: `name: demo
services:
  app:
    image: alpine
    volumes:
      - /cache
`,
			assert: func(t *testing.T, _ string, calls [][]string) {
				t.Helper()
				if len(calls) != 3 || !reflect.DeepEqual(calls[0], []string{"list", "--format", "json", "--all"}) {
					t.Fatalf("calls = %#v, want container list, volume create, run", calls)
				}
				created := calls[1]
				if len(created) != 7 || !reflect.DeepEqual(created[:4], []string{"volume", "create", "--label", accProjectLabel + "=demo"}) {
					t.Fatalf("create args = %#v", created)
				}
				volumeName := created[6]
				if !regexp.MustCompile(`^acc-demo-anon-[a-f0-9]{32}$`).MatchString(volumeName) {
					t.Fatalf("anonymous volume name = %q", volumeName)
				}
				if created[5] != accVolumeLabel+"="+volumeName {
					t.Fatalf("anonymous volume label = %q", created[5])
				}
				if !hasArgument(calls[2], "type=volume,source="+volumeName+",target=/cache") {
					t.Fatalf("run args = %#v", calls[2])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectDir, composePath := writeVolumeCompose(t, tt.contents)
			var calls [][]string
			run := func(_ context.Context, args ...string) (string, error) {
				calls = append(calls, append([]string(nil), args...))
				switch {
				case len(args) >= 2 && args[0] == "volume" && args[1] == "inspect":
					return "volume not found", errors.New("command failed")
				case reflect.DeepEqual(args, []string{"list", "--format", "json", "--all"}):
					return "[]", nil
				default:
					return "", nil
				}
			}
			client := newVolumeTestClient(run)

			if err := client.Up(context.Background(), composePath, ParseOptions{}, UpOptions{}); err != nil {
				t.Fatalf("Up() error = %v", err)
			}
			tt.assert(t, projectDir, calls)
		})
	}
}

func TestDownVolumesKeepsDetachedAnonymousVolumes(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `name: demo
services:
  app:
    image: alpine
`)
	const named = "demo_pgdata"
	const currentAnonymous = "acc-demo-anon-0123456789abcdef0123456789abcdef"
	const detachedAnonymous = "acc-demo-anon-fedcba9876543210fedcba9876543210"
	var calls [][]string
	run := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		switch {
		case reflect.DeepEqual(args, []string{"list", "--format", "json", "--all"}):
			return `[{"configuration":{"id":"demo_app_1","labels":{"` + accProjectLabel + `":"demo","` + accServiceLabel + `":"app"}}}]`, nil
		case reflect.DeepEqual(args, []string{"inspect", "demo_app_1"}):
			return `[{"id":"demo_app_1","configuration":{"id":"demo_app_1","mounts":[{"type":{"volume":{"name":"` + currentAnonymous + `"}}}]}}]`, nil
		case reflect.DeepEqual(args, []string{"volume", "list", "--format", "json"}):
			return `[
  {"id":"` + named + `","configuration":{"name":"` + named + `","labels":{"` + accProjectLabel + `":"demo","` + accVolumeLabel + `":"pgdata"}}},
  {"id":"` + currentAnonymous + `","configuration":{"name":"` + currentAnonymous + `","labels":{"` + accProjectLabel + `":"demo","` + accVolumeLabel + `":"` + currentAnonymous + `"}}},
  {"id":"` + detachedAnonymous + `","configuration":{"name":"` + detachedAnonymous + `","labels":{"` + accProjectLabel + `":"demo","` + accVolumeLabel + `":"` + detachedAnonymous + `"}}},
  {"id":"foreign","configuration":{"name":"foreign","labels":{"` + accProjectLabel + `":"other","` + accVolumeLabel + `":"foreign"}}}
]`, nil
		default:
			return "", nil
		}
	}

	client := newVolumeTestClient(run)
	if err := client.Down(context.Background(), composePath, ParseOptions{}, DownOptions{Volumes: true}); err != nil {
		t.Fatalf("Down() error = %v", err)
	}
	wantDelete := []string{"volume", "delete", currentAnonymous, named}
	if !hasCall(calls, wantDelete) {
		t.Fatalf("Down() calls = %#v, want %#v", calls, wantDelete)
	}
	for _, call := range calls {
		if len(call) >= 3 && call[0] == "volume" && call[1] == "delete" && hasArgument(call, detachedAnonymous) {
			t.Fatalf("Down() calls = %#v, must retain detached anonymous volume %q", calls, detachedAnonymous)
		}
	}
}

func TestUpDoesNotCreateAnotherAnonymousVolumeForExistingService(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `name: demo
services:
  app:
    image: alpine
    volumes:
      - /cache
`)
	run := func(_ context.Context, args ...string) (string, error) {
		if reflect.DeepEqual(args, []string{"list", "--format", "json", "--all"}) {
			return `[{"configuration":{"id":"demo_app_1","labels":{"` + accProjectLabel + `":"demo","` + accServiceLabel + `":"app"}}}]`, nil
		}
		t.Fatalf("unexpected runtime call: %#v", args)
		return "", nil
	}

	if err := newVolumeTestClient(run).Up(context.Background(), composePath, ParseOptions{}, UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
}

func newVolumeTestClient(run func(context.Context, ...string) (string, error)) *ComposeClient {
	return &ComposeClient{containerClient: &container.Client{
		Container: container.NewContainerClient(run, nil),
		Volumes:   container.NewVolumeClient(run),
	}}
}

func writeVolumeCompose(t *testing.T, contents string) (string, string) {
	t.Helper()
	projectDir := t.TempDir()
	composePath := filepath.Join(projectDir, "compose.yaml")
	if err := os.WriteFile(composePath, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return projectDir, composePath
}

func hasArgument(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func hasCall(calls [][]string, want []string) bool {
	for _, call := range calls {
		if reflect.DeepEqual(call, want) {
			return true
		}
	}
	return false
}
