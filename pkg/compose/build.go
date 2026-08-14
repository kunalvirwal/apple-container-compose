package compose

import (
	"context"
	"io"
	"path/filepath"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/kunalvirwal/apple-container-compose/pkg/container"
)

// BuildOptions controls which services are built and where build logs are written.
type BuildOptions struct {
	// Services limits build to the selected services. Empty means all services.
	Services []string
	// Output receives streaming build output.
	Output io.Writer
}

// Build loads a compose project and builds images for services that declare build config.
func (c *ComposeClient) BuildImages(ctx context.Context, path string, parseOpts ParseOptions, opts BuildOptions) error {
	if c.containerClient == nil {
		return ErrContainerClientNil
	}

	project, err := c.loadProject(ctx, path, parseOpts)
	if err != nil {
		return err
	}

	services, err := generateServiceOrder(project, opts.Services)
	if err != nil {
		return err
	}

	for _, serviceName := range services {
		svc, err := project.GetService(serviceName)
		if err != nil {
			return err
		}
		if svc.Build == nil {
			continue
		}

		containerBuildOptions := toImageBuildOptions(project, serviceName, svc)

		if _, err := c.containerClient.Images.Build(ctx, containerBuildOptions, opts.Output); err != nil {
			return err
		}
	}

	return nil
}

// toImageBuildOptions converts a compose service build section into container build options.
func toImageBuildOptions(project *types.Project, serviceName string, service types.ServiceConfig) container.BuildOptions {
	containerBuildOptions := container.BuildOptions{
		Tag:     service.Image,
		NoCache: service.Build.NoCache,
		Pull:    service.Build.Pull,
	}

	if len(service.Build.Platforms) > 0 {
		containerBuildOptions.Platform = service.Build.Platforms[0]
	} else if service.Platform != "" {
		containerBuildOptions.Platform = service.Platform
	}

	if len(service.Build.Args) > 0 {
		args := map[string]string{}
		for key, val := range service.Build.Args {
			if val != nil {
				args[key] = *val
			}
		}
		containerBuildOptions.BuildArgs = args
	}

	containerBuildOptions.ContextDir = resolveBuildContextDir(project, service.Build.Context)
	containerBuildOptions.File = resolveBuildDockerfilePath(containerBuildOptions.ContextDir, service.Build.Dockerfile)

	if containerBuildOptions.Tag == "" {
		containerBuildOptions.Tag = containerName(project.Name, serviceName)
	}

	return containerBuildOptions
}

// resolveBuildContextDir returns an absolute build context path.
func resolveBuildContextDir(project *types.Project, contextDir string) string {
	if contextDir == "" {
		contextDir = "."
	}
	if filepath.IsAbs(contextDir) {
		return filepath.Clean(contextDir)
	}
	if project != nil && project.WorkingDir != "" {
		return filepath.Clean(filepath.Join(project.WorkingDir, contextDir))
	}
	return filepath.Clean(contextDir)
}

// resolveBuildDockerfilePath resolves a Dockerfile path against the build context.
func resolveBuildDockerfilePath(contextDir string, dockerfile string) string {
	if dockerfile == "" {
		return ""
	}
	if filepath.IsAbs(dockerfile) {
		return filepath.Clean(dockerfile)
	}
	return filepath.Clean(filepath.Join(contextDir, dockerfile))
}
