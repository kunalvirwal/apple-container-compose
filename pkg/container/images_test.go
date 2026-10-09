package container

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestImageBuildStreamsPlainProgress(t *testing.T) {
	for _, runErr := range []error{nil, context.Canceled} {
		name := "success"
		if runErr != nil {
			name = "cancellation"
		}
		t.Run(name, func(t *testing.T) {
			contextDir := t.TempDir()
			dockerfile := filepath.Join(contextDir, "Dockerfile")
			if err := os.WriteFile(dockerfile, []byte("FROM scratch\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			client := NewImageClient(nil, func(_ context.Context, out io.Writer, args ...string) (string, error) {
				want := []string{"build", "--progress", "plain", "--tag", "app:latest", "--file", dockerfile, contextDir}
				if !reflect.DeepEqual(args, want) {
					t.Fatalf("args = %#v, want %#v", args, want)
				}
				progress := "#1 loading Dockerfile\n#2 building image\nsha256:built\n"
				if _, err := io.WriteString(out, progress); err != nil {
					t.Fatal(err)
				}
				if output.String() != progress {
					t.Fatal("build progress was not forwarded before the command finished")
				}
				return progress, runErr
			})
			id, err := client.Build(context.Background(), BuildOptions{ContextDir: contextDir, Tag: "app:latest"}, &output)
			if !errors.Is(err, runErr) || id != "sha256:built" {
				t.Fatalf("Build() = %q, %v; want sha256:built, %v", id, err, runErr)
			}
		})
	}
}

func TestImageInspectSummary(t *testing.T) {
	tests := []struct {
		name       string
		output     string
		runErr     error
		want       ImageSummary
		wantExists bool
	}{
		{
			name:       "descriptor",
			output:     `{"mediaType":"application/vnd.oci.image.index.v1+json","digest":"sha256:abc","size":123}`,
			want:       ImageSummary{Digest: "sha256:abc"},
			wantExists: true,
		},
		{
			name:       "nested descriptor",
			output:     `[{"descriptor":{"digest":"sha256:def"}}]`,
			want:       ImageSummary{Digest: "sha256:def"},
			wantExists: true,
		},
		{
			name:       "not found",
			output:     "image not found",
			runErr:     errors.New("command failed"),
			wantExists: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewImageClient(func(_ context.Context, args ...string) (string, error) {
				if want := []string{"image", "inspect", "alpine"}; !reflect.DeepEqual(args, want) {
					t.Fatalf("args = %#v, want %#v", args, want)
				}
				return tt.output, tt.runErr
			}, nil)
			got, exists, err := client.InspectSummary(context.Background(), "alpine")
			if err != nil {
				t.Fatalf("InspectSummary() error = %v", err)
			}
			if exists != tt.wantExists || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("InspectSummary() = %#v, %v; want %#v, %v", got, exists, tt.want, tt.wantExists)
			}
		})
	}
}
