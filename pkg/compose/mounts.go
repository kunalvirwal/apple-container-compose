package compose

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	units "github.com/docker/go-units"
	"github.com/kunalvirwal/apple-container-compose/pkg/container"
)

func prepareServiceBindMounts(project *types.Project, services []string) error {
	for _, serviceName := range services {
		service, err := project.GetService(serviceName)
		if err != nil {
			return err
		}
		for _, volume := range service.Volumes {
			if volume.Type != types.VolumeTypeBind {
				continue
			}
			mount, err := bindMountFromVolume(project.WorkingDir, serviceName, volume)
			if err != nil {
				return err
			}
			if err := ensureBindSource(serviceName, volume, mount.Source); err != nil {
				return err
			}
		}
	}
	return nil
}

func mountsForService(project *types.Project, service types.ServiceConfig, anonymousVolumes []string) ([]container.Mount, error) {
	if len(service.Volumes) == 0 && len(service.Tmpfs) == 0 {
		return nil, nil
	}

	mounts := make([]container.Mount, 0, len(service.Volumes)+len(service.Tmpfs))
	anonymousIndex := 0
	for _, volume := range service.Volumes {
		var (
			mount container.Mount
			err   error
		)
		switch volume.Type {
		case types.VolumeTypeBind:
			mount, err = bindMountFromVolume(project.WorkingDir, service.Name, volume)
		case types.VolumeTypeVolume:
			anonymousSource := ""
			if volume.Source == "" {
				if anonymousIndex >= len(anonymousVolumes) {
					return nil, fmt.Errorf("anonymous volume for service %q was not prepared", service.Name)
				}
				anonymousSource = anonymousVolumes[anonymousIndex]
				anonymousIndex++
			}
			mount, err = namedVolumeMount(project, service.Name, volume, anonymousSource)
		case types.VolumeTypeTmpfs:
			mount, err = tmpfsMount(service.Name, volume)
		default:
			err = fmt.Errorf("%w: service %q volume type %q", ErrUnsupportedFeature, service.Name, volume.Type)
		}
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, mount)
	}
	shortTmpfsMounts, err := tmpfsShorthandMounts(service.Name, service.Tmpfs, nil)
	if err != nil {
		return nil, err
	}
	mounts = append(mounts, shortTmpfsMounts...)
	return mounts, nil
}

func validateServiceMounts(project *types.Project, services []string, onWarning, onFatalWarning func(string)) error {
	if err := validateShortSyntaxVolumeOptions(project, services, onFatalWarning); err != nil {
		return err
	}
	for _, serviceName := range services {
		service, err := project.GetService(serviceName)
		if err != nil {
			return err
		}
		if len(service.VolumesFrom) > 0 {
			return fatalUnsupportedFeature(onFatalWarning, fmt.Sprintf("service %q volumes_from is not supported by ACC, please define volume mounts separately", service.Name))
		}
		for _, volume := range service.Volumes {
			warnUnsupportedConsistency(service.Name, volume, onWarning)
			warnUnsupportedBindSELinux(service.Name, volume, onWarning)
			if err := validateBindMountFeatures(service.Name, volume, onFatalWarning); err != nil {
				return err
			}
			switch volume.Type {
			case types.VolumeTypeBind:
				if _, err := bindMountFromVolume(project.WorkingDir, service.Name, volume); err != nil {
					return err
				}
			case types.VolumeTypeVolume:
				if err := validateServiceVolumeFeatures(service.Name, volume, onFatalWarning); err != nil {
					return err
				}
				if err := validateServiceVolume(project, service.Name, volume); err != nil {
					return err
				}
			case types.VolumeTypeTmpfs:
				if err := validateTmpfsVolume(service.Name, volume, onFatalWarning); err != nil {
					return err
				}
			default:
				return fatalUnsupportedFeature(onFatalWarning, fmt.Sprintf("service %q volume type %q is not supported by ACC", service.Name, volume.Type))
			}
		}
		if _, err := tmpfsShorthandMounts(service.Name, service.Tmpfs, onFatalWarning); err != nil {
			return err
		}
	}
	return nil
}

func validateBindMountFeatures(serviceName string, volume types.ServiceVolumeConfig, onFatalWarning func(string)) error {
	if volume.Bind == nil || volume.Bind.Propagation == "" {
		return nil
	}
	return fatalUnsupportedFeature(onFatalWarning, fmt.Sprintf("service %q bind.propagation %q is not supported by ACC", serviceName, volume.Bind.Propagation))
}

func validateShortSyntaxVolumeOptions(project *types.Project, services []string, onFatalWarning func(string)) error {
	selected := make(map[string]struct{}, len(services))
	for _, serviceName := range services {
		selected[serviceName] = struct{}{}
	}
	for _, composePath := range project.ComposeFiles {
		document, err := readRawComposeDocument(composePath)
		if err != nil {
			return err
		}
		for serviceName, rawService := range document.Services {
			if _, ok := selected[serviceName]; !ok {
				continue
			}
			for _, rawVolume := range rawService.Volumes {
				definition, ok := rawVolume.(string)
				if !ok {
					continue
				}
				for _, option := range shortSyntaxVolumeOptions(definition) {
					if isSupportedShortSyntaxVolumeOption(option) {
						continue
					}
					return fatalUnsupportedFeature(onFatalWarning, fmt.Sprintf("service %q volume option %q is not supported by ACC", serviceName, option))
				}
			}
		}
	}
	return nil
}

func isSupportedShortSyntaxVolumeOption(option string) bool {
	switch option {
	case "ro", "rw", "nocopy", "cached", "delegated", "consistent",
		types.PropagationRPrivate, types.PropagationPrivate, types.PropagationRShared,
		types.PropagationShared, types.PropagationRSlave, types.PropagationSlave,
		types.SELinuxShared, types.SELinuxPrivate:
		return true
	default:
		return false
	}
}

func warnUnsupportedConsistency(serviceName string, volume types.ServiceVolumeConfig, onWarning func(string)) {
	if onWarning == nil || volume.Consistency == "" {
		return
	}
	onWarning(fmt.Sprintf("service %q volume consistency %q is not supported by ACC and will be ignored", serviceName, volume.Consistency))
}

func warnUnsupportedBindSELinux(serviceName string, volume types.ServiceVolumeConfig, onWarning func(string)) {
	if onWarning == nil || volume.Bind == nil || volume.Bind.SELinux == "" {
		return
	}
	onWarning(fmt.Sprintf("service %q bind.selinux %q is not supported by ACC and will be ignored", serviceName, volume.Bind.SELinux))
}

func validateProjectVolumeLabels(project *types.Project, services []string, onFatalWarning func(string)) error {
	volumeNames, err := referencedNamedVolumes(project, services)
	if err != nil {
		return err
	}
	for _, volumeName := range volumeNames {
		volume := project.Volumes[volumeName]
		if volume.Driver != "" && volume.Driver != "local" {
			return fatalUnsupportedFeature(onFatalWarning, fmt.Sprintf("volume %q driver %q is not supported by ACC; only the local driver is supported", volumeName, volume.Driver))
		}
		if err := validateVolumeDriverOptions(volumeName, volume.DriverOpts, onFatalWarning); err != nil {
			return err
		}
		if _, err := configuredVolumeLabels(project, volumeName, onFatalWarning); err != nil {
			return err
		}
	}
	return nil
}

func validateSharedNamedVolumes(project *types.Project, onFatalWarning func(string)) error {
	type usage struct {
		readOnly bool
	}
	uses := make(map[string]map[string]usage)
	for _, serviceName := range project.ServiceNames() {
		service, err := project.GetService(serviceName)
		if err != nil {
			return err
		}
		for _, volume := range service.Volumes {
			if volume.Type != types.VolumeTypeVolume || volume.Source == "" {
				continue
			}
			if err := validateServiceVolumeFeatures(serviceName, volume, onFatalWarning); err != nil {
				return err
			}
			if err := validateServiceVolume(project, serviceName, volume); err != nil {
				return err
			}
			if uses[volume.Source] == nil {
				uses[volume.Source] = make(map[string]usage)
			}
			current, exists := uses[volume.Source][serviceName]
			if !exists {
				uses[volume.Source][serviceName] = usage{readOnly: volume.ReadOnly}
				continue
			}
			uses[volume.Source][serviceName] = usage{readOnly: current.readOnly && volume.ReadOnly}
		}
	}

	volumeNames := make([]string, 0, len(uses))
	for volumeName := range uses {
		volumeNames = append(volumeNames, volumeName)
	}
	sort.Strings(volumeNames)
	for _, volumeName := range volumeNames {
		serviceUses := uses[volumeName]
		if len(serviceUses) < 2 {
			continue
		}
		for _, use := range serviceUses {
			if !use.readOnly {
				return fatalUnsupportedFeature(onFatalWarning, fmt.Sprintf("named volume %q is mounted by multiple services, but Apple Container only supports read-only shared named volumes; use a bind mount for shared writable storage", volumeName))
			}
		}
	}
	return nil
}

func validateServiceVolumeFeatures(serviceName string, volume types.ServiceVolumeConfig, onFatalWarning func(string)) error {
	if volume.Volume == nil {
		return nil
	}
	if volume.Volume.NoCopy {
		return fatalUnsupportedFeature(onFatalWarning, fmt.Sprintf("service %q volume.nocopy is not supported by ACC", serviceName))
	}
	if volume.Volume.Subpath != "" {
		return fatalUnsupportedFeature(onFatalWarning, fmt.Sprintf("service %q volume.subpath is not supported by ACC", serviceName))
	}
	keys := make([]string, 0, len(volume.Volume.Labels))
	for key := range volume.Volume.Labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if isACCVolumeLabel(key) {
			return fatalUnsupportedFeature(onFatalWarning, fmt.Sprintf("service %q volume label %q is reserved for ACC", serviceName, key))
		}
	}
	return nil
}

func configuredVolumeLabels(project *types.Project, volumeName string, onFatalWarning func(string)) (map[string]string, error) {
	labels := make(map[string]string)
	volume := project.Volumes[volumeName]
	if err := mergeVolumeLabels(labels, volumeName, volume.Labels, onFatalWarning); err != nil {
		return nil, err
	}
	for _, serviceName := range project.ServiceNames() {
		service, err := project.GetService(serviceName)
		if err != nil {
			return nil, err
		}
		for _, serviceVolume := range service.Volumes {
			if serviceVolume.Type != types.VolumeTypeVolume || serviceVolume.Source != volumeName || serviceVolume.Volume == nil {
				continue
			}
			if err := mergeVolumeLabels(labels, volumeName, serviceVolume.Volume.Labels, onFatalWarning); err != nil {
				return nil, err
			}
		}
	}
	return labels, nil
}

func mergeVolumeLabels(destination map[string]string, volumeName string, source map[string]string, onFatalWarning func(string)) error {
	keys := make([]string, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if isACCVolumeLabel(key) {
			return fatalUnsupportedFeature(onFatalWarning, fmt.Sprintf("volume %q label %q is reserved for ACC", volumeName, key))
		}
		value := source[key]
		if existing, exists := destination[key]; exists && existing != value {
			return fatalUnsupportedFeature(onFatalWarning, fmt.Sprintf("volume %q label %q has conflicting values %q and %q", volumeName, key, existing, value))
		}
		destination[key] = value
	}
	return nil
}

func isACCVolumeLabel(key string) bool {
	return key == accProjectLabel || key == accVolumeLabel
}

func tmpfsMount(serviceName string, volume types.ServiceVolumeConfig) (container.Mount, error) {
	if err := validateTmpfsVolume(serviceName, volume, nil); err != nil {
		return container.Mount{}, err
	}

	mount := container.Mount{
		Type:     container.MountTypeTmpfs,
		Target:   filepath.Clean(volume.Target),
		ReadOnly: volume.ReadOnly,
	}
	if volume.Tmpfs != nil {
		mount.TmpfsSize = uint64(volume.Tmpfs.Size)
		if volume.Tmpfs.Mode != 0 {
			mount.TmpfsMode = strconv.FormatUint(uint64(volume.Tmpfs.Mode), 10)
		}
	}
	return mount, nil
}

func tmpfsShorthandMounts(serviceName string, definitions types.StringList, onFatalWarning func(string)) ([]container.Mount, error) {
	mounts := make([]container.Mount, 0, len(definitions))
	for _, definition := range definitions {
		mount, err := tmpfsShorthandMount(serviceName, definition, onFatalWarning)
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, mount)
	}
	return mounts, nil
}

func tmpfsShorthandMount(serviceName, definition string, onFatalWarning func(string)) (container.Mount, error) {
	target, options, hasOptions := strings.Cut(definition, ":")
	if !filepath.IsAbs(target) {
		return container.Mount{}, fmt.Errorf("service %q tmpfs target %q must be an absolute path", serviceName, target)
	}

	mount := container.Mount{Type: container.MountTypeTmpfs, Target: filepath.Clean(target)}
	if !hasOptions || options == "" {
		return mount, nil
	}
	for _, option := range strings.Split(options, ",") {
		option = strings.TrimSpace(option)
		key, value, hasValue := strings.Cut(option, "=")
		switch key {
		case "ro":
			if hasValue {
				return container.Mount{}, fmt.Errorf("%w: service %q tmpfs option %q must not have a value", ErrUnsupportedFeature, serviceName, key)
			}
			mount.ReadOnly = true
		case "rw":
			if hasValue {
				return container.Mount{}, fmt.Errorf("%w: service %q tmpfs option %q must not have a value", ErrUnsupportedFeature, serviceName, key)
			}
			mount.ReadOnly = false
		case "size":
			if !hasValue || value == "" {
				return container.Mount{}, fatalUnsupportedFeature(onFatalWarning, fmt.Sprintf("service %q tmpfs size must have a value", serviceName))
			}
			size, err := units.RAMInBytes(value)
			if err != nil || size < 0 {
				return container.Mount{}, fatalUnsupportedFeature(onFatalWarning, fmt.Sprintf("service %q tmpfs size %q is invalid", serviceName, value))
			}
			mount.TmpfsSize = uint64(size)
		case "mode":
			if !hasValue || value == "" {
				return container.Mount{}, fatalUnsupportedFeature(onFatalWarning, fmt.Sprintf("service %q tmpfs mode must have a value", serviceName))
			}
			if _, err := strconv.ParseUint(value, 8, 32); err != nil {
				return container.Mount{}, fatalUnsupportedFeature(onFatalWarning, fmt.Sprintf("service %q tmpfs mode %q is invalid", serviceName, value))
			}
			mount.TmpfsMode = value
		case "uid", "gid", "uuid", "guid":
			return container.Mount{}, fatalUnsupportedFeature(onFatalWarning, "tmpfs uid/gid ownership is not supported by Apple container")
		default:
			return container.Mount{}, fatalUnsupportedFeature(onFatalWarning, fmt.Sprintf("service %q tmpfs option %q is not supported", serviceName, key))
		}
	}
	return mount, nil
}

func validateTmpfsVolume(serviceName string, volume types.ServiceVolumeConfig, onFatalWarning func(string)) error {
	if volume.Source != "" {
		return fatalUnsupportedFeature(onFatalWarning, fmt.Sprintf("service %q tmpfs source is not supported", serviceName))
	}
	if !filepath.IsAbs(volume.Target) {
		return fmt.Errorf("service %q tmpfs target %q must be an absolute path", serviceName, volume.Target)
	}
	return nil
}

func fatalUnsupportedFeature(onFatalWarning func(string), message string) error {
	if onFatalWarning != nil {
		onFatalWarning(message)
	}
	return fmt.Errorf("%w: %s", ErrUnsupportedFeature, message)
}

type upVolumePlan struct {
	anonymousVolumes map[string][]string
	create           []container.VolumeCreateOptions
}

func planNamedVolumes(ctx context.Context, project *types.Project, services []string, client *container.VolumeClient, onWarning func(string), existingAnonymousVolumes map[string]map[string]string, renewAnonymousVolumes bool) (upVolumePlan, error) {
	plan := upVolumePlan{anonymousVolumes: make(map[string][]string)}
	volumeNames, err := referencedNamedVolumes(project, services)
	if err != nil {
		return upVolumePlan{}, err
	}
	for _, volumeName := range volumeNames {
		volume := project.Volumes[volumeName]
		runtimeName := composeVolumeName(project, volumeName, volume)
		summary, exists, err := client.InspectSummary(ctx, runtimeName)
		if err != nil {
			return upVolumePlan{}, fmt.Errorf("inspect volume %q: %w", runtimeName, err)
		}
		if volume.External {
			if !exists {
				return upVolumePlan{}, fmt.Errorf("external volume %q does not exist", runtimeName)
			}
			continue
		}
		if exists {
			warnUnmanagedVolume(onWarning, project.Name, volumeName, runtimeName, summary)
			continue
		}

		labels, err := configuredVolumeLabels(project, volumeName, nil)
		if err != nil {
			return upVolumePlan{}, err
		}
		labels[accProjectLabel] = project.Name
		labels[accVolumeLabel] = volumeName
		plan.create = append(plan.create, container.VolumeCreateOptions{
			Name:    runtimeName,
			Labels:  labels,
			Options: volume.DriverOpts,
		})
	}

	for _, serviceName := range services {
		service, err := project.GetService(serviceName)
		if err != nil {
			return upVolumePlan{}, err
		}
		for _, volume := range service.Volumes {
			if volume.Type != types.VolumeTypeVolume || volume.Source != "" {
				continue
			}
			if err := validateServiceVolume(project, serviceName, volume); err != nil {
				return upVolumePlan{}, err
			}
			if !renewAnonymousVolumes {
				if runtimeName := existingAnonymousVolumes[serviceName][volume.Target]; runtimeName != "" {
					summary, exists, err := client.InspectSummary(ctx, runtimeName)
					if err != nil {
						return upVolumePlan{}, fmt.Errorf("inspect anonymous volume %q: %w", runtimeName, err)
					}
					if exists && isAnonymousVolume(project.Name, summary) {
						plan.anonymousVolumes[serviceName] = append(plan.anonymousVolumes[serviceName], runtimeName)
						continue
					}
				}
			}
			runtimeName, err := anonymousVolumeName(project.Name)
			if err != nil {
				return upVolumePlan{}, err
			}
			labels := make(map[string]string, 2)
			if volume.Volume != nil {
				for key, value := range volume.Volume.Labels {
					labels[key] = value
				}
			}
			labels[accProjectLabel] = project.Name
			labels[accVolumeLabel] = runtimeName
			plan.create = append(plan.create, container.VolumeCreateOptions{
				Name:   runtimeName,
				Labels: labels,
			})
			plan.anonymousVolumes[serviceName] = append(plan.anonymousVolumes[serviceName], runtimeName)
		}
	}
	return plan, nil
}

func (plan upVolumePlan) apply(ctx context.Context, client *container.VolumeClient) error {
	for _, options := range plan.create {
		if _, err := client.Create(ctx, options); err != nil {
			return fmt.Errorf("create volume %q: %w", options.Name, err)
		}
	}
	return nil
}

func warnUnmanagedVolume(onWarning func(string), projectName, volumeName, runtimeName string, summary container.VolumeSummary) {
	if onWarning == nil {
		return
	}
	if summary.Labels[accProjectLabel] == projectName && summary.Labels[accVolumeLabel] == volumeName {
		return
	}
	onWarning(fmt.Sprintf("Volume %q already exists but is not managed by ACC; it will be mounted, but 'acc down --volumes' will not remove it. Declare it external or recreate it with labels to allow ACC manage it.", runtimeName))
}

func referencedNamedVolumes(project *types.Project, services []string) ([]string, error) {
	names := make(map[string]struct{})
	for _, serviceName := range services {
		service, err := project.GetService(serviceName)
		if err != nil {
			return nil, err
		}
		for _, volume := range service.Volumes {
			if volume.Type != types.VolumeTypeVolume {
				continue
			}
			if volume.Source == "" {
				continue
			}
			if err := validateServiceVolume(project, serviceName, volume); err != nil {
				return nil, err
			}
			names[volume.Source] = struct{}{}
		}
	}

	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

func namedVolumeMount(project *types.Project, serviceName string, volume types.ServiceVolumeConfig, anonymousSource string) (container.Mount, error) {
	if err := validateServiceVolume(project, serviceName, volume); err != nil {
		return container.Mount{}, err
	}
	if volume.Source == "" {
		if anonymousSource == "" {
			return container.Mount{}, fmt.Errorf("anonymous volume for service %q was not prepared", serviceName)
		}
		return container.Mount{
			Type:     container.MountTypeVolume,
			Source:   anonymousSource,
			Target:   filepath.Clean(volume.Target),
			ReadOnly: volume.ReadOnly,
		}, nil
	}
	definition := project.Volumes[volume.Source]
	return container.Mount{
		Type:     container.MountTypeVolume,
		Source:   composeVolumeName(project, volume.Source, definition),
		Target:   filepath.Clean(volume.Target),
		ReadOnly: volume.ReadOnly,
	}, nil
}

func validateServiceVolume(project *types.Project, serviceName string, volume types.ServiceVolumeConfig) error {
	if volume.Volume != nil && (volume.Volume.NoCopy || volume.Volume.Subpath != "") {
		return fmt.Errorf("%w: service %q volume options", ErrUnsupportedFeature, serviceName)
	}
	if volume.Source != "" {
		if _, ok := project.Volumes[volume.Source]; !ok {
			return fmt.Errorf("%w: service %q references undefined volume %q", ErrUnsupportedFeature, serviceName, volume.Source)
		}
	}
	if !filepath.IsAbs(volume.Target) {
		return fmt.Errorf("service %q volume target %q must be an absolute path", serviceName, volume.Target)
	}
	return nil
}

func composeVolumeName(project *types.Project, volumeName string, volume types.VolumeConfig) string {
	if volume.Name != "" {
		return volume.Name
	}
	return project.Name + "_" + volumeName
}

func validateVolumeDriverOptions(volumeName string, options types.Options, onFatalWarning func(string)) error {
	keys := make([]string, 0, len(options))
	for key := range options {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := options[key]
		switch key {
		case "size":
			if err := validateVolumeSize(value); err != nil {
				return fatalUnsupportedFeature(onFatalWarning, fmt.Sprintf("volume %q driver_opts.size %q is invalid: %v", volumeName, value, err))
			}
		case "journal":
			if err := validateVolumeJournal(value); err != nil {
				return fatalUnsupportedFeature(onFatalWarning, fmt.Sprintf("volume %q driver_opts.journal %q is invalid: %v", volumeName, value, err))
			}
		default:
			return fatalUnsupportedFeature(onFatalWarning, fmt.Sprintf("volume %q driver_opts.%s is not supported by ACC", volumeName, key))
		}
	}
	return nil
}

func validateVolumeSize(value string) error {
	size, err := units.RAMInBytes(value)
	if err != nil || size < 1024*1024 {
		return fmt.Errorf("must be at least 1MiB with an optional K, M, G, T, or P suffix")
	}
	return nil
}

func validateVolumeJournal(value string) error {
	mode, size, hasSize := strings.Cut(value, ":")
	if mode != "ordered" && mode != "writeback" && mode != "journal" {
		return fmt.Errorf("mode must be writeback, ordered, or journal")
	}
	if !hasSize {
		return nil
	}
	if size == "" || strings.Contains(size, ":") {
		return fmt.Errorf("journal size must be specified once after ':'")
	}
	return validateVolumeSize(size)
}

func anonymousVolumeName(projectName string) (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate anonymous volume name: %w", err)
	}
	return "acc-" + projectName + "-anon-" + hex.EncodeToString(bytes), nil
}

func bindMountFromVolume(workingDir, serviceName string, volume types.ServiceVolumeConfig) (container.Mount, error) {
	if volume.Type != types.VolumeTypeBind {
		return container.Mount{}, fmt.Errorf("%w: service %q volume type %q", ErrUnsupportedFeature, serviceName, volume.Type)
	}
	source, err := resolveBindSource(workingDir, volume.Source)
	if err != nil {
		return container.Mount{}, fmt.Errorf("service %q bind source %q: %w", serviceName, volume.Source, err)
	}
	if !filepath.IsAbs(volume.Target) {
		return container.Mount{}, fmt.Errorf("service %q bind target %q must be an absolute path", serviceName, volume.Target)
	}
	return container.Mount{
		Type:     container.MountTypeBind,
		Source:   source,
		Target:   filepath.Clean(volume.Target),
		ReadOnly: volume.ReadOnly,
	}, nil
}

func resolveBindSource(workingDir, source string) (string, error) {
	if source == "" {
		return "", fmt.Errorf("must not be empty")
	}
	if filepath.IsAbs(source) {
		return filepath.Clean(source), nil
	}
	if workingDir == "" {
		return "", fmt.Errorf("cannot resolve relative path without a project working directory")
	}
	if !filepath.IsAbs(workingDir) {
		absoluteWorkingDir, err := filepath.Abs(workingDir)
		if err != nil {
			return "", fmt.Errorf("resolve project working directory %q: %w", workingDir, err)
		}
		workingDir = absoluteWorkingDir
	}
	return filepath.Clean(filepath.Join(workingDir, source)), nil
}

func ensureBindSource(serviceName string, volume types.ServiceVolumeConfig, source string) error {
	info, err := os.Stat(source)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("service %q bind source %q must be a directory", serviceName, source)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("inspect bind source %q for service %q: %w", source, serviceName, err)
	}
	if !bindCreatesHostPath(volume) {
		return fmt.Errorf("bind source %q for service %q does not exist and bind.create_host_path is false", source, serviceName)
	}
	if err := os.MkdirAll(source, 0o755); err != nil {
		return fmt.Errorf("create bind source %q for service %q: %w", source, serviceName, err)
	}
	return nil
}

func bindCreatesHostPath(volume types.ServiceVolumeConfig) bool {
	return volume.Bind == nil || bool(volume.Bind.CreateHostPath)
}
