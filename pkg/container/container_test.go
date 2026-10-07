package container

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"
)

func TestContainerRunNameservers(t *testing.T) {
	var calls [][]string
	client := NewContainerClient(func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		return "", nil
	}, nil)

	if _, err := client.Run(context.Background(), "alpine", CreateOptions{
		Name:        "demo_app_1",
		Nameservers: []string{"1.1.1.1", "::ffff:8.8.8.8", "2001:4860:4860::8888"},
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := [][]string{{
		"run", "--name", "demo_app_1",
		"--dns", "1.1.1.1",
		"--dns", "8.8.8.8",
		"--dns", "2001:4860:4860::8888",
		"-d", "alpine",
	}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestValidateRunOptionsMemoryMinimum(t *testing.T) {
	tests := []struct {
		memory  string
		wantErr bool
	}{
		{memory: "128M", wantErr: true},
		{memory: "199M", wantErr: true},
		{memory: "204799K", wantErr: true},
		{memory: "200M"},
		{memory: "204800K"},
		{memory: "1G"},
	}
	for _, tt := range tests {
		t.Run(tt.memory, func(t *testing.T) {
			err := ValidateRunOptions("alpine", CreateOptions{Memory: tt.memory})
			if tt.wantErr && !errors.Is(err, ErrInvalidOptions) {
				t.Fatalf("ValidateRunOptions() error = %v, want ErrInvalidOptions", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("ValidateRunOptions() error = %v, want nil", err)
			}
		})
	}
}

func TestNormalizeNameservers(t *testing.T) {
	for _, raw := range [][]string{
		{""}, {"not-an-address"}, {"0.0.0.0"}, {"::"},
		{"127.0.0.1"}, {"::1"},
		{"224.0.0.1"}, {"ff02::1"}, {"169.254.0.1"}, {"fe80::1"},
		{"fe80::1%eth0"},
	} {
		if _, err := NormalizeNameservers(raw); !errors.Is(err, ErrInvalidOptions) {
			t.Fatalf("NormalizeNameservers(%v) error = %v, want ErrInvalidOptions", raw, err)
		}
	}

	got, err := NormalizeNameservers([]string{"10.0.0.53", "::ffff:8.8.8.8"})
	if err != nil || !reflect.DeepEqual(got, []string{"10.0.0.53", "8.8.8.8"}) {
		t.Fatalf("NormalizeNameservers() = %v, %v", got, err)
	}
}

func TestInspectDetailsNetworkAddresses(t *testing.T) {
	client := NewContainerClient(func(_ context.Context, args ...string) (string, error) {
		want := []string{"inspect", "demo_app_1"}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("args = %#v, want %#v", args, want)
		}
		return `[
  {
    "configuration": {"id": "demo_app_1", "image": {"reference": "alpine:latest", "descriptor": {"digest": "sha256:used"}}, "mounts": []},
    "status": {"networks": [
      {"network": "demo_backend", "ipv4Address": "192.168.65.3/24", "ipv6Address": "fde2:6153:8fcf:4e0a::3/64"},
      {"network": "demo_default", "ipv4Address": "192.168.64.3/24"}
    ]}
  }
]`, nil
	}, nil)

	details, err := client.InspectDetails(context.Background(), []string{"demo_app_1"})
	if err != nil {
		t.Fatalf("InspectDetails() error = %v", err)
	}
	want := []ContainerDetails{{
		ID:          "demo_app_1",
		ImageDigest: "sha256:used",
		VolumeNames: []string{},
		Networks: []NetworkAttachment{
			{Name: "demo_backend", Addresses: []netip.Addr{netip.MustParseAddr("192.168.65.3"), netip.MustParseAddr("fde2:6153:8fcf:4e0a::3")}},
			{Name: "demo_default", Addresses: []netip.Addr{netip.MustParseAddr("192.168.64.3")}},
		},
	}}
	if !reflect.DeepEqual(details, want) {
		t.Fatalf("InspectDetails() = %#v, want %#v", details, want)
	}
}

func TestInspectDetailsVolumeMountTarget(t *testing.T) {
	client := NewContainerClient(func(_ context.Context, args ...string) (string, error) {
		return `[{"configuration":{"id":"demo_app_1","mounts":[{"type":{"volume":{"name":"acc-demo-anon-123"}},"destination":"/cache","options":["ro"]}]}}]`, nil
	}, nil)
	details, err := client.InspectDetails(context.Background(), []string{"demo_app_1"})
	if err != nil {
		t.Fatalf("InspectDetails() error = %v", err)
	}
	want := []ContainerMount{{
		Type:     MountTypeVolume,
		Source:   "acc-demo-anon-123",
		Target:   "/cache",
		ReadOnly: true,
	}}
	if len(details) != 1 || !reflect.DeepEqual(details[0].Mounts, want) {
		t.Fatalf("InspectDetails() = %#v, want mounts %#v", details, want)
	}
}

func TestContainerStart(t *testing.T) {
	var got []string
	client := NewContainerClient(func(_ context.Context, args ...string) (string, error) {
		got = append([]string(nil), args...)
		return "demo_app_1", nil
	}, nil)
	if _, err := client.Start(context.Background(), "demo_app_1"); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if want := []string{"start", "demo_app_1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}
