package container

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

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
