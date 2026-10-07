package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/kunalvirwal/apple-container-compose/pkg/container"
)

const (
	progressProjectLabel = "io.github.kunalvirwal.acc.project"
	progressServiceLabel = "io.github.kunalvirwal.acc.service"
	progressRoleLabel    = "io.github.kunalvirwal.acc.role"
	progressHashLabel    = "io.github.kunalvirwal.acc.config-hash"
	progressImageLabel   = "io.github.kunalvirwal.acc.image-id"
	progressNetworkLabel = "io.github.kunalvirwal.acc.network"
)

// upReporter is CLI-only: it observes completed runtime changes without
// participating in Compose's decisions or changing SDK behavior.
type upReporter struct {
	runtime   *container.Client
	output    io.Writer
	disabled  bool
	activeDNS string
}

type upSnapshot struct {
	containers map[string]container.ContainerSummary
	running    map[string]bool
	details    map[string]container.ContainerDetails
	networks   map[string]container.NetworkSummary
	volumes    map[string]container.VolumeSummary
}

func newUpReporter(runtime *container.Client, output io.Writer) *upReporter {
	if runtime == nil || output == nil {
		return nil
	}
	return &upReporter{runtime: runtime, output: output}
}

func (r *upReporter) line(format string, args ...any) {
	if r != nil {
		_ = writeReconcileLine(r.output, fmt.Sprintf(format, args...))
	}
}

func (r *upReporter) capture(ctx context.Context, project string, networks, volumes, containers bool) (upSnapshot, bool) {
	if r == nil || r.disabled {
		return upSnapshot{}, false
	}
	observeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	snapshot, err := r.readSnapshot(observeCtx, project, networks, volumes, containers)
	if err != nil {
		r.line("Detailed reconciliation status unavailable: %v", err)
		r.disabled = true
		return upSnapshot{}, false
	}
	return snapshot, true
}

func (r *upReporter) readSnapshot(ctx context.Context, project string, includeNetworks, includeVolumes, includeContainers bool) (upSnapshot, error) {
	snapshot := upSnapshot{
		containers: make(map[string]container.ContainerSummary),
		running:    make(map[string]bool),
		details:    make(map[string]container.ContainerDetails),
		networks:   make(map[string]container.NetworkSummary),
		volumes:    make(map[string]container.VolumeSummary),
	}
	if includeNetworks {
		networks, err := r.runtime.Networks.ListSummaries(ctx)
		if err != nil {
			return upSnapshot{}, fmt.Errorf("list networks: %w", err)
		}
		for _, item := range networks {
			snapshot.networks[item.Name] = item
		}
	}
	if includeVolumes {
		volumes, err := r.runtime.Volumes.ListSummaries(ctx)
		if err == nil {
			for _, item := range volumes {
				snapshot.volumes[item.Name] = item
			}
		}
	}
	if !includeContainers || project == "" {
		return snapshot, nil
	}
	all, err := r.runtime.Container.ListSummaries(ctx, container.ListOptions{All: true})
	if err != nil {
		return upSnapshot{}, fmt.Errorf("list project containers: %w", err)
	}
	ids := make([]string, 0)
	for _, item := range all {
		if item.Labels[progressProjectLabel] != project {
			continue
		}
		snapshot.containers[item.ID] = item
		ids = append(ids, item.ID)
	}
	running, err := r.runtime.Container.ListSummaries(ctx, container.ListOptions{})
	if err != nil {
		return upSnapshot{}, fmt.Errorf("list running containers: %w", err)
	}
	for _, item := range running {
		if _, found := snapshot.containers[item.ID]; found {
			snapshot.running[item.ID] = true
		}
	}
	if len(ids) == 0 {
		return snapshot, nil
	}
	sort.Strings(ids)
	details, err := r.runtime.Container.InspectDetails(ctx, ids)
	if err != nil {
		return snapshot, nil
	}
	for _, item := range details {
		snapshot.details[item.ID] = item
	}
	return snapshot, nil
}

func (r *upReporter) prepared(project string, requiredNetworks []string, before, after upSnapshot) {
	if r == nil || r.disabled {
		return
	}
	for _, name := range sortedSnapshotNetworks(after.networks) {
		item := after.networks[name]
		if item.Labels[progressProjectLabel] == project && item.Labels[progressNetworkLabel] != "" {
			if _, existed := before.networks[name]; !existed {
				r.line("Created network %s", name)
			}
		}
	}
	for _, name := range requiredNetworks {
		if _, existed := before.networks[name]; existed {
			r.line("Reusing network %s - already exists", name)
		}
	}
	for _, name := range sortedSnapshotVolumes(after.volumes) {
		if after.volumes[name].Labels[progressProjectLabel] != project {
			continue
		}
		if _, existed := before.volumes[name]; !existed {
			r.line("Created volume %s", name)
		}
	}
}

func (r *upReporter) services(serviceNames []string, forceBuild, renewAnonymousVolumes bool, before, after upSnapshot) {
	if r == nil || r.disabled {
		return
	}
	selected := make(map[string]bool)
	for _, service := range serviceNames {
		selected[service] = true
		oldID, old, existed := serviceContainer(before, service)
		newID, current, found := serviceContainer(after, service)
		if !found {
			continue
		}
		switch {
		case !existed:
			r.line("Created service %s - no previous ACC container", service)
		case old.Labels[progressHashLabel] != current.Labels[progressHashLabel] || old.Labels[progressImageLabel] != current.Labels[progressImageLabel]:
			r.line("Recreated service %s - %s", service, serviceChangeReason(old, current, before.details[oldID], after.details[newID], renewAnonymousVolumes))
		case !before.running[oldID] && after.running[newID]:
			r.line("Started service %s - existing configuration matches", service)
		case !after.running[newID]:
			r.line("Service %s exists but is not running - check the runtime state", service)
		case forceBuild && old.Labels[progressImageLabel] == "" && current.Labels[progressImageLabel] == "":
			r.line("Service %s is running - configuration matches; rebuild outcome is not identifiable from runtime metadata", service)
		default:
			r.line("Reusing service %s - configuration matches and container is running", service)
		}
	}
	for _, id := range sortedSnapshotContainers(before.containers) {
		item := before.containers[id]
		service := item.Labels[progressServiceLabel]
		if service == "" || selected[service] {
			continue
		}
		if _, stillExists := after.containers[id]; !stillExists {
			r.line("Removed orphan container %s - --remove-orphans is set", id)
		} else {
			r.line("Keeping project container %s - service was not selected or orphan removal is disabled", id)
		}
	}
}

func serviceContainer(snapshot upSnapshot, service string) (string, container.ContainerSummary, bool) {
	for id, item := range snapshot.containers {
		if item.Labels[progressServiceLabel] == service {
			return id, item, true
		}
	}
	return "", container.ContainerSummary{}, false
}

func serviceChangeReason(before, after container.ContainerSummary, oldDetails, newDetails container.ContainerDetails, renewAnonymousVolumes bool) string {
	reasons := make([]string, 0, 3)
	if oldImage, newImage := before.Labels[progressImageLabel], after.Labels[progressImageLabel]; oldImage != "" && newImage != "" && oldImage != newImage {
		reasons = append(reasons, "local image ID changed")
	}
	if oldDetails.ID != "" && newDetails.ID != "" && !sameNames(networkNames(oldDetails.Networks), networkNames(newDetails.Networks)) {
		reasons = append(reasons, "network membership changed")
	}
	if oldDetails.ID != "" && newDetails.ID != "" && !sameNames(mountKeys(oldDetails.Mounts, false), mountKeys(newDetails.Mounts, false)) {
		if renewAnonymousVolumes && sameNames(mountKeys(oldDetails.Mounts, true), mountKeys(newDetails.Mounts, true)) {
			reasons = append(reasons, "anonymous volumes renewed (--renew-anon-volumes)")
		} else {
			reasons = append(reasons, "mounts changed")
		}
	}
	if len(reasons) > 0 {
		return strings.Join(reasons, "; ")
	}
	if before.Labels[progressHashLabel] == "" {
		return "previous container had no ACC configuration fingerprint"
	}
	return "runtime configuration fingerprint changed"
}

func (r *upReporter) coreDNSStarted(desiredNetworks []string, before, after upSnapshot) {
	if r == nil || r.disabled {
		return
	}
	old := coreDNSContainers(before)
	current := coreDNSContainers(after)
	for _, id := range current {
		if sameNames(networkNames(after.details[id].Networks), desiredNetworks) && after.running[id] {
			r.activeDNS = id
			break
		}
	}
	for _, id := range current {
		if _, existed := before.containers[id]; !existed {
			reason := "no previous project CoreDNS container"
			if len(old) > 0 {
				reason = "network set changed; older CoreDNS stays available during service migration"
			}
			r.line("Created CoreDNS %s - %s", id, reason)
		} else if id == r.activeDNS {
			if before.running[id] {
				r.line("Reusing CoreDNS %s - required network set matches", id)
			} else {
				r.line("Started CoreDNS %s - network set matches but it was stopped", id)
			}
		}
	}
}

func (r *upReporter) coreDNSFinished(before, after upSnapshot, directDNS bool) {
	if r == nil || r.disabled {
		return
	}
	for _, id := range coreDNSContainers(before) {
		if _, exists := after.containers[id]; !exists {
			reason := "services now use the current CoreDNS generation"
			if directDNS {
				reason = "selected services now use direct Compose DNS"
			}
			r.line("Removed CoreDNS %s - %s", id, reason)
		} else if directDNS || (r.activeDNS != "" && id != r.activeDNS) {
			r.line("Keeping CoreDNS %s - unselected or orphaned containers may still depend on it", id)
		}
	}
}

func (r *upReporter) networksRemoved(project string, before, after upSnapshot) {
	if r == nil || r.disabled {
		return
	}
	for _, name := range sortedSnapshotNetworks(before.networks) {
		item := before.networks[name]
		if item.Labels[progressProjectLabel] != project || item.Labels[progressNetworkLabel] == "" {
			continue
		}
		if _, exists := after.networks[name]; !exists {
			r.line("Removed network %s - no longer required by the project", name)
		}
	}
}

func coreDNSContainers(snapshot upSnapshot) []string {
	ids := make([]string, 0)
	for id, item := range snapshot.containers {
		if item.Labels[progressRoleLabel] == "coredns" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func networkNames(attachments []container.NetworkAttachment) []string {
	names := make([]string, 0, len(attachments))
	for _, item := range attachments {
		names = append(names, item.Name)
	}
	return names
}

func mountKeys(mounts []container.ContainerMount, maskAnonymousSource bool) []string {
	keys := make([]string, 0, len(mounts))
	for _, item := range mounts {
		source := item.Source
		if maskAnonymousSource && item.Type == container.MountTypeVolume && strings.HasPrefix(source, "acc-") && strings.Contains(source, "-anon-") {
			source = "<anonymous>"
		}
		keys = append(keys, fmt.Sprintf("%s:%s:%s:%t", item.Type, source, item.Target, item.ReadOnly))
	}
	return keys
}

func sameNames(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	sort.Strings(left)
	sort.Strings(right)
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sortedSnapshotContainers(items map[string]container.ContainerSummary) []string {
	names := make([]string, 0, len(items))
	for name := range items {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedSnapshotNetworks(items map[string]container.NetworkSummary) []string {
	names := make([]string, 0, len(items))
	for name := range items {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedSnapshotVolumes(items map[string]container.VolumeSummary) []string {
	names := make([]string, 0, len(items))
	for name := range items {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
