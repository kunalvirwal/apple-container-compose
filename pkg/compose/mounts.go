package compose

import (
	"fmt"
	"os"
	"path/filepath"

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

func bindMountsForService(workingDir string, service types.ServiceConfig) ([]container.Mount, error) {
	if len(service.Volumes) == 0 {
		return nil, nil
	}

	mounts := make([]container.Mount, 0, len(service.Volumes))
	for _, volume := range service.Volumes {
		mount, err := bindMountFromVolume(workingDir, service.Name, volume)
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, mount)
	}
	return mounts, nil
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
