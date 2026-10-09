package compose

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/kunalvirwal/apple-container-compose/pkg/container"
)

func TestLogsSynchronizesConcurrentServiceStreams(t *testing.T) {
	_, path := writeVolumeCompose(t, "name: demo\nservices:\n  app:\n    image: alpine\n  worker:\n    image: alpine\n")
	stream := func(_ context.Context, out io.Writer, args ...string) (string, error) {
		for i := 0; i < 20; i++ {
			if _, err := io.WriteString(out, "ready\n"); err != nil {
				return "", err
			}
		}
		return "", nil
	}
	client := &ComposeClient{containerClient: &container.Client{
		Container: container.NewContainerClient(nil, stream),
	}}
	var output bytes.Buffer
	if err := client.Logs(context.Background(), path, ParseOptions{}, LogsOptions{Output: &output}); err != nil {
		t.Fatal(err)
	}
	for _, service := range []string{"app", "worker"} {
		if got := strings.Count(output.String(), "["+service+"] ready\n"); got != 20 {
			t.Fatalf("%s has %d complete lines, want 20: %q", service, got, output.String())
		}
	}
}
