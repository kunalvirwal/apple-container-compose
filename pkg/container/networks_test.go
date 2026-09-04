package container

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestNetworkClientCreate(t *testing.T) {
	var calls [][]string
	client := NewNetworkClient(func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		return "demo_default\n", nil
	})

	output, err := client.Create(context.Background(), NetworkCreateOptions{
		Name: "demo_default",
		Labels: map[string]string{
			"io.github.kunalvirwal.acc.project": "demo",
			"io.github.kunalvirwal.acc.network": "default",
		},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if output != "demo_default\n" {
		t.Fatalf("Create() output = %q", output)
	}
	want := []string{
		"network", "create",
		"--label", "io.github.kunalvirwal.acc.network=default",
		"--label", "io.github.kunalvirwal.acc.project=demo",
		"demo_default",
	}
	if !reflect.DeepEqual(calls, [][]string{want}) {
		t.Fatalf("calls = %#v, want %#v", calls, [][]string{want})
	}
}

func TestNetworkClientExists(t *testing.T) {
	tests := []struct {
		name   string
		output string
		err    error
		exists bool
	}{
		{name: "exists", output: `[{"configuration":{"name":"demo_default"}}]`, exists: true},
		{name: "missing", output: "network not found", err: errors.New("command failed"), exists: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls [][]string
			client := NewNetworkClient(func(_ context.Context, args ...string) (string, error) {
				calls = append(calls, append([]string(nil), args...))
				return tt.output, tt.err
			})

			exists, err := client.Exists(context.Background(), "demo_default")
			if err != nil {
				t.Fatalf("Exists() error = %v", err)
			}
			if exists != tt.exists {
				t.Fatalf("Exists() = %t, want %t", exists, tt.exists)
			}
			want := [][]string{{"network", "inspect", "demo_default"}}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls = %#v, want %#v", calls, want)
			}
		})
	}
}

func TestContainerRunNetworks(t *testing.T) {
	var calls [][]string
	client := NewContainerClient(func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		return "", nil
	}, nil)

	if _, err := client.Run(context.Background(), "alpine", CreateOptions{
		Name:     "demo_app_1",
		Networks: []string{"demo_backend", "demo_frontend"},
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := [][]string{{"run", "--name", "demo_app_1", "--network", "demo_backend", "--network", "demo_frontend", "-d", "alpine"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestNetworkClientDelete(t *testing.T) {
	var calls [][]string
	client := NewNetworkClient(func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		return "", nil
	})

	if _, err := client.Delete(context.Background(), []string{"demo_default"}); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	want := [][]string{{"network", "delete", "demo_default"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestNetworkClientListSummaries(t *testing.T) {
	client := NewNetworkClient(func(_ context.Context, args ...string) (string, error) {
		want := []string{"network", "list", "--format", "json"}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("args = %#v, want %#v", args, want)
		}
		return `[{"configuration":{"name":"demo_backend","labels":{"project":"demo"}}}]`, nil
	})

	summaries, err := client.ListSummaries(context.Background())
	if err != nil {
		t.Fatalf("ListSummaries() error = %v", err)
	}
	want := []NetworkSummary{{Name: "demo_backend", Labels: map[string]string{"project": "demo"}}}
	if !reflect.DeepEqual(summaries, want) {
		t.Fatalf("ListSummaries() = %#v, want %#v", summaries, want)
	}
}
