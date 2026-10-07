package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kunalvirwal/apple-container-compose/pkg/container"
)

func TestUpReporterExplainsConfirmedReconciliation(t *testing.T) {
	var output bytes.Buffer
	reporter := &upReporter{output: &output}
	ownedNetwork := func(name, logical string) container.NetworkSummary {
		return container.NetworkSummary{Name: name, Labels: map[string]string{
			progressProjectLabel: "demo", progressNetworkLabel: logical,
		}}
	}
	service := func(id, name, hash string) container.ContainerSummary {
		return container.ContainerSummary{ID: id, Labels: map[string]string{
			progressProjectLabel: "demo", progressServiceLabel: name, progressHashLabel: hash,
		}}
	}
	dns := func(id string) container.ContainerSummary {
		return container.ContainerSummary{ID: id, Labels: map[string]string{
			progressProjectLabel: "demo", progressRoleLabel: "coredns",
		}}
	}
	before := upSnapshot{
		networks: map[string]container.NetworkSummary{"demo_old": ownedNetwork("demo_old", "old")},
		volumes:  map[string]container.VolumeSummary{},
	}
	prepared := upSnapshot{
		networks: map[string]container.NetworkSummary{
			"demo_old": ownedNetwork("demo_old", "old"),
			"demo_new": ownedNetwork("demo_new", "new"),
		},
		volumes: map[string]container.VolumeSummary{
			"demo_data": {Name: "demo_data", Labels: map[string]string{progressProjectLabel: "demo"}},
		},
		containers: map[string]container.ContainerSummary{
			"demo_app_1": service("demo_app_1", "app", "old-hash"),
			"demo_old_1": service("demo_old_1", "old", "old-hash"),
			"old-dns-id": dns("old-dns-id"),
		},
		running: map[string]bool{"demo_app_1": true, "demo_old_1": true, "old-dns-id": true},
		details: map[string]container.ContainerDetails{
			"demo_app_1": {ID: "demo_app_1", Networks: []container.NetworkAttachment{{Name: "demo_old"}}},
			"old-dns-id": {ID: "old-dns-id", Networks: []container.NetworkAttachment{{Name: "demo_old"}}},
		},
	}
	reporter.prepared("demo", []string{"demo_new"}, before, prepared)

	dnsStarted := prepared
	dnsStarted.containers = cloneContainers(prepared.containers)
	dnsStarted.containers["new-dns-id"] = dns("new-dns-id")
	dnsStarted.running = map[string]bool{"demo_app_1": true, "demo_old_1": true, "old-dns-id": true, "new-dns-id": true}
	dnsStarted.details = cloneDetails(prepared.details)
	dnsStarted.details["new-dns-id"] = container.ContainerDetails{ID: "new-dns-id", Networks: []container.NetworkAttachment{{Name: "demo_new"}}}
	reporter.coreDNSStarted([]string{"demo_new"}, prepared, dnsStarted)

	started := dnsStarted
	started.containers = cloneContainers(dnsStarted.containers)
	started.containers["demo_app_1"] = service("demo_app_1", "app", "new-hash")
	delete(started.containers, "demo_old_1")
	started.running = map[string]bool{"demo_app_1": true, "old-dns-id": true, "new-dns-id": true}
	started.details = cloneDetails(dnsStarted.details)
	started.details["demo_app_1"] = container.ContainerDetails{ID: "demo_app_1", Networks: []container.NetworkAttachment{{Name: "demo_new"}}}
	reporter.services([]string{"app"}, false, false, dnsStarted, started)

	retired := started
	retired.containers = cloneContainers(started.containers)
	delete(retired.containers, "old-dns-id")
	reporter.coreDNSFinished(started, retired, false)
	cleaned := retired
	cleaned.networks = map[string]container.NetworkSummary{"demo_new": ownedNetwork("demo_new", "new")}
	reporter.networksRemoved("demo", retired, cleaned)

	want := []string{
		"Created network demo_new",
		"Created volume demo_data",
		"Created CoreDNS new-dns-id - network set changed; older CoreDNS stays available during service migration",
		"Recreated service app - network membership changed",
		"Removed orphan container demo_old_1 - --remove-orphans is set",
		"Removed CoreDNS old-dns-id - services now use the current CoreDNS generation",
		"Removed network demo_old - no longer required by the project",
	}
	text := output.String()
	last := -1
	for _, line := range want {
		index := strings.Index(text, line)
		if index <= last {
			t.Fatalf("output = %q; missing or out-of-order line %q", text, line)
		}
		last = index
	}
}

func TestUpReporterKeepsOldCoreDNSForOrphans(t *testing.T) {
	var output bytes.Buffer
	reporter := &upReporter{output: &output, activeDNS: "new-dns-id"}
	old := container.ContainerSummary{ID: "old-dns-id", Labels: map[string]string{progressRoleLabel: "coredns"}}
	newDNS := container.ContainerSummary{ID: "new-dns-id", Labels: map[string]string{progressRoleLabel: "coredns"}}
	before := upSnapshot{containers: map[string]container.ContainerSummary{"old-dns-id": old, "new-dns-id": newDNS}}
	reporter.coreDNSFinished(before, before, false)
	if got := output.String(); !strings.Contains(got, "Keeping CoreDNS old-dns-id - unselected or orphaned containers may still depend on it") || strings.Contains(got, "Keeping CoreDNS new-dns-id") {
		t.Fatalf("output = %q, want only old CoreDNS retained", got)
	}
}

func TestUpReporterServiceDecisions(t *testing.T) {
	service := func(hash string) container.ContainerSummary {
		return container.ContainerSummary{ID: "demo_app_1", Labels: map[string]string{
			progressServiceLabel: "app", progressHashLabel: hash,
		}}
	}
	tests := []struct {
		name   string
		before upSnapshot
		after  upSnapshot
		build  bool
		want   string
	}{
		{
			name:   "create",
			before: upSnapshot{containers: map[string]container.ContainerSummary{}},
			after:  upSnapshot{containers: map[string]container.ContainerSummary{"demo_app_1": service("new")}},
			want:   "Created service app - no previous ACC container",
		},
		{
			name:   "start stopped",
			before: upSnapshot{containers: map[string]container.ContainerSummary{"demo_app_1": service("same")}},
			after:  upSnapshot{containers: map[string]container.ContainerSummary{"demo_app_1": service("same")}, running: map[string]bool{"demo_app_1": true}},
			want:   "Started service app - existing configuration matches",
		},
		{
			name:   "reuse running",
			before: upSnapshot{containers: map[string]container.ContainerSummary{"demo_app_1": service("same")}, running: map[string]bool{"demo_app_1": true}},
			after:  upSnapshot{containers: map[string]container.ContainerSummary{"demo_app_1": service("same")}, running: map[string]bool{"demo_app_1": true}},
			want:   "Reusing service app - configuration matches and container is running",
		},
		{
			name:   "recreate without inspect details",
			before: upSnapshot{containers: map[string]container.ContainerSummary{"demo_app_1": service("old")}, running: map[string]bool{"demo_app_1": true}},
			after:  upSnapshot{containers: map[string]container.ContainerSummary{"demo_app_1": service("new")}, running: map[string]bool{"demo_app_1": true}},
			want:   "Recreated service app - runtime configuration fingerprint changed",
		},
		{
			name:   "build without image identity",
			before: upSnapshot{containers: map[string]container.ContainerSummary{"demo_app_1": service("same")}, running: map[string]bool{"demo_app_1": true}},
			after:  upSnapshot{containers: map[string]container.ContainerSummary{"demo_app_1": service("same")}, running: map[string]bool{"demo_app_1": true}},
			build:  true,
			want:   "Service app is running - configuration matches; rebuild outcome is not identifiable from runtime metadata",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			reporter := &upReporter{output: &output}
			reporter.services([]string{"app"}, test.build, false, test.before, test.after)
			if got := strings.TrimSpace(output.String()); got != "[ACC] "+test.want {
				t.Fatalf("output = %q, want %q", got, "[ACC] "+test.want)
			}
		})
	}
}

func TestReconcileLineRespectsNoColor(t *testing.T) {
	var plain, colored bytes.Buffer
	if err := writeReconcileLine(newServiceLogWriter(&plain, false), "Reusing service app"); err != nil {
		t.Fatal(err)
	}
	if err := writeReconcileLine(newServiceLogWriter(&colored, true), "Reusing service app"); err != nil {
		t.Fatal(err)
	}
	if got, want := plain.String(), "[ACC] Reusing service app\n"; got != want {
		t.Fatalf("plain output = %q, want %q", got, want)
	}
	if got, want := colored.String(), ansiPurple+"[ACC] Reusing service app\n"+ansiReset; got != want {
		t.Fatalf("colored output = %q, want purple progress %q", got, want)
	}
}

func TestServiceChangeReasonForRenewedAnonymousVolumes(t *testing.T) {
	old := container.ContainerDetails{ID: "demo_app_1", Mounts: []container.ContainerMount{
		{Type: container.MountTypeVolume, Source: "acc-demo-anon-old", Target: "/cache"},
	}}
	current := container.ContainerDetails{ID: "demo_app_1", Mounts: []container.ContainerMount{
		{Type: container.MountTypeVolume, Source: "acc-demo-anon-new", Target: "/cache"},
	}}
	if got, want := serviceChangeReason(container.ContainerSummary{}, container.ContainerSummary{}, old, current, true), "anonymous volumes renewed (--renew-anon-volumes)"; got != want {
		t.Fatalf("serviceChangeReason() = %q, want %q", got, want)
	}
	if got, want := serviceChangeReason(container.ContainerSummary{}, container.ContainerSummary{}, old, current, false), "mounts changed"; got != want {
		t.Fatalf("serviceChangeReason() = %q, want %q", got, want)
	}
	current.Mounts = append(current.Mounts, container.ContainerMount{Type: container.MountTypeBind, Source: "/tmp/data", Target: "/data"})
	if got, want := serviceChangeReason(container.ContainerSummary{}, container.ContainerSummary{}, old, current, true), "mounts changed"; got != want {
		t.Fatalf("serviceChangeReason() = %q, want %q", got, want)
	}
}

func cloneContainers(items map[string]container.ContainerSummary) map[string]container.ContainerSummary {
	clone := make(map[string]container.ContainerSummary, len(items))
	for id, item := range items {
		clone[id] = item
	}
	return clone
}

func cloneDetails(items map[string]container.ContainerDetails) map[string]container.ContainerDetails {
	clone := make(map[string]container.ContainerDetails, len(items))
	for id, item := range items {
		clone[id] = item
	}
	return clone
}
