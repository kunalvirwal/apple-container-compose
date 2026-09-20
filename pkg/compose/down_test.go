package compose

import (
	"context"
	"reflect"
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
