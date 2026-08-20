package compose

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/compose-spec/compose-go/v2/types"
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

func mountsForService(project *types.Project, service types.ServiceConfig) ([]container.Mount, error) {
	if len(service.Volumes) == 0 {
		return nil, nil
	}

	mounts := make([]container.Mount, 0, len(service.Volumes))
	for _, volume := range service.Volumes {
		var (
			mount container.Mount
			err   error
		)
		switch volume.Type {
		case types.VolumeTypeBind:
			mount, err = bindMountFromVolume(project.WorkingDir, service.Name, volume)
		case types.VolumeTypeVolume:
			mount, err = namedVolumeMount(project, service.Name, volume)
		default:
			err = fmt.Errorf("%w: service %q volume type %q", ErrUnsupportedFeature, service.Name, volume.Type)
		}
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, mount)
	}
	return mounts, nil
}

func validateServiceMounts(project *types.Project, services []string) error {
	for _, serviceName := range services {
		service, err := project.GetService(serviceName)
		if err != nil {
			return err
		}
		if _, err := mountsForService(project, service); err != nil {
			return err
		}
	}
	return nil
}

func prepareNamedVolumes(ctx context.Context, project *types.Project, services []string, client *container.VolumeClient, onWarning func(string)) error {
	volumeNames, err := referencedNamedVolumes(project, services)
	if err != nil {
		return err
	}
	for _, volumeName := range volumeNames {
		volume := project.Volumes[volumeName]
		runtimeName := composeVolumeName(project, volumeName, volume)
		summary, exists, err := client.InspectSummary(ctx, runtimeName)
		if err != nil {
			return fmt.Errorf("inspect volume %q: %w", runtimeName, err)
		}
		if volume.External {
			if !exists {
				return fmt.Errorf("external volume %q does not exist", runtimeName)
			}
			continue
		}
		if volume.Driver != "" && volume.Driver != "local" {
			return fmt.Errorf("%w: volume %q driver %q", ErrUnsupportedFeature, volumeName, volume.Driver)
		}
		if err := validateVolumeDriverOptions(volumeName, volume.DriverOpts); err != nil {
			return err
		}

		if exists {
			warnUnmanagedVolume(onWarning, project.Name, volumeName, runtimeName, summary)
			continue
		}

		labels := make(map[string]string, len(volume.Labels)+2)
		for key, value := range volume.Labels {
			labels[key] = value
		}
		labels[accProjectLabel] = project.Name
		labels[accVolumeLabel] = volumeName
		if _, err := client.Create(ctx, container.VolumeCreateOptions{
			Name:    runtimeName,
			Labels:  labels,
			Options: volume.DriverOpts,
		}); err != nil {
			return fmt.Errorf("create volume %q: %w", runtimeName, err)
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
	onWarning(fmt.Sprintf("Volume %q already exists but is not managed by ACC; it will be mounted, but acc down --volumes will not remove it. Declare it external or recreate it with labels to allow ACC manage it.", runtimeName))
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
				return nil, fmt.Errorf("%w: anonymous volume for service %q", ErrUnsupportedFeature, serviceName)
			}
			if _, err := namedVolumeMount(project, serviceName, volume); err != nil {
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

func namedVolumeMount(project *types.Project, serviceName string, volume types.ServiceVolumeConfig) (container.Mount, error) {
	if volume.Source == "" {
		return container.Mount{}, fmt.Errorf("%w: anonymous volume for service %q", ErrUnsupportedFeature, serviceName)
	}
	definition, ok := project.Volumes[volume.Source]
	if !ok {
		return container.Mount{}, fmt.Errorf("%w: service %q references undefined volume %q", ErrUnsupportedFeature, serviceName, volume.Source)
	}
	if !filepath.IsAbs(volume.Target) {
		return container.Mount{}, fmt.Errorf("service %q volume target %q must be an absolute path", serviceName, volume.Target)
	}
	return container.Mount{
		Type:     container.MountTypeVolume,
		Source:   composeVolumeName(project, volume.Source, definition),
		Target:   filepath.Clean(volume.Target),
		ReadOnly: volume.ReadOnly,
	}, nil
}

func composeVolumeName(project *types.Project, volumeName string, volume types.VolumeConfig) string {
	if volume.Name != "" {
		return volume.Name
	}
	return project.Name + "_" + volumeName
}

func validateVolumeDriverOptions(volumeName string, options types.Options) error {
	for option := range options {
		if option != "size" && option != "journal" {
			return fmt.Errorf("%w: volume %q driver_opts.%s", ErrUnsupportedFeature, volumeName, option)
		}
	}
	return nil
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
