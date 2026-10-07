package compose

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/kunalvirwal/apple-container-compose/pkg/container"
)

func TestDownContainerIDsDoesNotSelectProjectInfrastructure(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `name: demo
services:
  app:
    image: alpine
`)
	client := &ComposeClient{}
	project, err := client.loadProject(context.Background(), composePath, ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}

	ids := downContainerIDs(project, []string{"app"}, []container.ContainerSummary{
		{ID: "demo_app_1", Labels: map[string]string{accProjectLabel: "demo", accServiceLabel: "app"}},
		{ID: "demo_old_1", Labels: map[string]string{accProjectLabel: "demo", accServiceLabel: "old"}},
		{ID: "demo-coredns", Labels: map[string]string{accProjectLabel: "demo", "io.github.kunalvirwal.acc.role": "coredns"}},
	}, true)

	want := []string{"demo_app_1", "demo_old_1"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("downContainerIDs() = %#v, want %#v", ids, want)
	}
}

func TestDownSelectedServicesVolumes(t *testing.T) {
	const anonymous = "acc-demo-anon-0123456789abcdef0123456789abcdef"
	const detachedAnonymous = "acc-demo-anon-fedcba9876543210fedcba9876543210"
	inspectionFailure := errors.New("inspect failed")
	tests := []struct {
		name               string
		volumes            bool
		apiAbsent          bool
		emptyMounts        bool
		retainedID         string
		retainedService    string
		retainedProject    string
		retainedVolumes    []string
		appearsAfterDelete bool
		removeOrphans      bool
		inspectErr         error
		incompleteInspect  bool
		wantErr            error
		wantDelete         []string
	}{
		{
			name:       "exclusive named and anonymous volumes",
			volumes:    true,
			wantDelete: []string{anonymous, "demo_private", "demo_shared"},
		},
		{
			name: "no volume flag retains all volumes",
		},
		{
			name:    "absent selected container retains detached volumes",
			volumes: true, apiAbsent: true,
		},
		{
			name:    "selected container without volumes retains detached volumes",
			volumes: true, emptyMounts: true,
		},
		{
			name:    "unselected service retains shared volume",
			volumes: true, retainedID: "demo_worker_1", retainedService: "worker", retainedProject: "demo",
			retainedVolumes: []string{"demo_shared"},
			wantDelete:      []string{anonymous, "demo_private"},
		},
		{
			name:    "stopped foreign container retains shared volume",
			volumes: true, retainedID: "foreign_worker", retainedService: "worker", retainedProject: "other",
			retainedVolumes: []string{"demo_shared", "demo_private"},
			wantDelete:      []string{anonymous},
		},
		{
			name:    "unlabelled container retains shared volume",
			volumes: true, retainedID: "manual",
			retainedVolumes: []string{"demo_shared"},
			wantDelete:      []string{anonymous, "demo_private"},
		},
		{
			name:    "retained orphan protects shared volume",
			volumes: true, retainedID: "demo_old_1", retainedService: "old", retainedProject: "demo",
			retainedVolumes: []string{"demo_shared"},
			wantDelete:      []string{anonymous, "demo_private"},
		},
		{
			name:    "removed orphan releases shared volume",
			volumes: true, retainedID: "demo_old_1", retainedService: "old", retainedProject: "demo", removeOrphans: true,
			retainedVolumes: []string{"demo_shared", "demo_orphan"},
			wantDelete:      []string{anonymous, "demo_orphan", "demo_private", "demo_shared"},
		},
		{
			name:    "removed dependent releases shared volume",
			volumes: true, retainedID: "demo_web_1", retainedService: "web", retainedProject: "demo",
			retainedVolumes: []string{"demo_shared", "demo_dependent"},
			wantDelete:      []string{anonymous, "demo_dependent", "demo_private", "demo_shared"},
		},
		{
			name:    "new reference after preparation protects volume",
			volumes: true, retainedID: "manual", appearsAfterDelete: true,
			retainedVolumes: []string{"demo_shared"},
			wantDelete:      []string{anonymous, "demo_private"},
		},
		{
			name:    "inspection failure prevents volume deletion",
			volumes: true, retainedID: "manual", inspectErr: inspectionFailure, wantErr: inspectionFailure,
		},
		{
			name:    "cancellation remains identifiable",
			volumes: true, retainedID: "manual", inspectErr: context.Canceled, wantErr: context.Canceled,
		},
		{
			name:    "missing inspection metadata prevents volume deletion",
			volumes: true, retainedID: "manual", incompleteInspect: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, composePath := writeVolumeCompose(t, `name: demo
services:
  api:
    image: alpine
  worker:
    image: alpine
  web:
    image: alpine
    depends_on: [api]
volumes:
  external:
    external: true
    name: demo_external
`)
			encode := func(value any) string {
				t.Helper()
				encoded, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				return string(encoded)
			}
			api := map[string]any{"configuration": map[string]any{
				"id": "demo_api_1", "labels": map[string]string{accProjectLabel: "demo", accServiceLabel: "api"},
			}}
			remaining := map[string]any{"configuration": map[string]any{
				"id": tt.retainedID, "labels": map[string]string{accProjectLabel: tt.retainedProject, accServiceLabel: tt.retainedService},
			}}
			mountsByID := map[string][]string{
				"demo_api_1":  {"demo_private", "demo_shared", anonymous, "demo_external", "foreign", "unowned"},
				tt.retainedID: tt.retainedVolumes,
			}
			if tt.emptyMounts {
				mountsByID["demo_api_1"] = nil
			}
			removed := make(map[string]bool)
			var calls [][]string
			run := func(_ context.Context, args ...string) (string, error) {
				calls = append(calls, append([]string(nil), args...))
				switch {
				case reflect.DeepEqual(args, []string{"list", "--format", "json", "--all"}):
					items := []any{}
					if !tt.apiAbsent && !removed["demo_api_1"] {
						items = append(items, api)
					}
					if tt.retainedID != "" && !removed[tt.retainedID] && (!tt.appearsAfterDelete || removed["demo_api_1"]) {
						items = append(items, remaining)
					}
					return encode(items), nil
				case args[0] == "inspect":
					if removed["demo_api_1"] {
						if tt.inspectErr != nil {
							return "", tt.inspectErr
						}
						if tt.incompleteInspect {
							return "[]", nil
						}
					}
					items := []any{}
					for _, id := range args[1:] {
						mounts := []any{}
						for _, name := range mountsByID[id] {
							mounts = append(mounts, map[string]any{"type": map[string]any{"volume": map[string]string{"name": name}}})
						}
						items = append(items, map[string]any{"configuration": map[string]any{"id": id, "mounts": mounts}})
					}
					return encode(items), nil
				case args[0] == "stop":
					return "", nil
				case args[0] == "delete":
					for _, id := range args[1:] {
						removed[id] = true
					}
					return "", nil
				case reflect.DeepEqual(args, []string{"volume", "list", "--format", "json"}):
					items := []any{}
					for _, name := range []string{"demo_private", "demo_shared", anonymous, detachedAnonymous, "demo_external", "demo_detached", "demo_orphan", "demo_dependent", "foreign", "unowned"} {
						labels := map[string]string{accProjectLabel: "demo", accVolumeLabel: name}
						if name == "foreign" {
							labels[accProjectLabel] = "other"
						} else if name == "unowned" {
							delete(labels, accVolumeLabel)
						}
						items = append(items, map[string]any{"configuration": map[string]any{"name": name, "labels": labels}})
					}
					return encode(items), nil
				case len(args) > 2 && args[0] == "volume" && args[1] == "delete":
					return "", nil
				default:
					t.Fatalf("unexpected runtime call: %#v", args)
					return "", nil
				}
			}
			err := newVolumeTestClient(run).Down(context.Background(), composePath, ParseOptions{}, DownOptions{
				Services: []string{"api"}, Volumes: tt.volumes, RemoveOrphans: tt.removeOrphans,
			})
			if tt.incompleteInspect {
				if err == nil || !strings.Contains(err.Error(), "missing container metadata") {
					t.Fatalf("Down() error = %v, want missing metadata error", err)
				}
			} else if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Down() error = %v, want %v", err, tt.wantErr)
			}
			var deletedVolumes []string
			for index, call := range calls {
				if len(call) > 2 && call[0] == "volume" && call[1] == "delete" {
					serviceDeleted := false
					for _, earlier := range calls[:index] {
						if earlier[0] == "delete" && hasArgument(earlier, "demo_api_1") {
							serviceDeleted = true
						}
					}
					if !serviceDeleted {
						t.Fatalf("volume deletion preceded service deletion: %#v", calls)
					}
					deletedVolumes = append(deletedVolumes, call[2:]...)
				}
			}
			wantDelete := append([]string(nil), tt.wantDelete...)
			sort.Strings(wantDelete)
			if !reflect.DeepEqual(deletedVolumes, wantDelete) {
				t.Fatalf("deleted volumes = %#v, want %#v; calls = %#v", deletedVolumes, wantDelete, calls)
			}
			if !tt.volumes {
				for _, call := range calls {
					if call[0] == "inspect" || call[0] == "volume" {
						t.Fatalf("volume cleanup without --volumes: %#v", call)
					}
				}
			}
		})
	}
}
