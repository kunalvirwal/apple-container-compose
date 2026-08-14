package compose

import (
	"context"
	"io"

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

		buildOpts := container.BuildOptions{
			Tag: svc.Image,
		}

		if svc.Build != nil {
			buildOpts.ContextDir = svc.Build.Context
			buildOpts.File = svc.Build.Dockerfile
			buildOpts.NoCache = svc.Build.NoCache
			buildOpts.Pull = svc.Build.Pull
			if len(svc.Build.Platforms) == 1 {
				buildOpts.Platform = svc.Build.Platforms[0]
			}

			args := map[string]string{}
			for key, val := range svc.Build.Args {
				if val != nil {
					args[key] = *val
				}
			}
			buildOpts.BuildArgs = args
		}

		if buildOpts.ContextDir == "" {
			buildOpts.ContextDir = "."
		}

		// buildOpts := toImageBuildOptions(svc)
		if buildOpts.Tag == "" {
			buildOpts.Tag = containerName(project.Name, serviceName)
		}

		if _, err := c.containerClient.Images.Build(ctx, buildOpts, opts.Output); err != nil {
			return err
		}
	}

	return nil
}
