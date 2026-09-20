// Package cli implements the acc command-line interface.
package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kunalvirwal/apple-container-compose/internal/state"
	"github.com/kunalvirwal/apple-container-compose/pkg/compose"
	"github.com/spf13/cobra"
)

type composeService interface {
	Up(context.Context, string, compose.ParseOptions, compose.UpOptions) error
	Down(context.Context, string, compose.ParseOptions, compose.DownOptions) error
	BuildImages(context.Context, string, compose.ParseOptions, compose.BuildOptions) error
	Logs(context.Context, string, compose.ParseOptions, compose.LogsOptions) error
}

type rootOptions struct {
	files            []string
	projectName      string
	projectDirectory string
	envFiles         []string
	noColor          bool
}

// NewRootCommand creates the root command for acc.
func NewRootCommand() *cobra.Command {
	return newRootCommand(newComposeClient, runUp)
}

func newRootCommand(newComposeClient func() (composeService, error), executeUp func(context.Context, string, compose.ParseOptions, compose.UpOptions, bool) error) *cobra.Command {
	opts := &rootOptions{}

	rootCmd := &cobra.Command{
		Use:           "acc",
		Short:         "Run Compose projects with Apple's container runtime",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	flags := rootCmd.PersistentFlags()
	// Cobra cannot inherit a -f file flag while logs also provides Docker
	// Compose's conventional -f/--follow flag, so the global file option uses
	// its long form only.
	flags.StringArrayVar(&opts.files, "file", nil, "Compose configuration file")
	flags.StringVarP(&opts.projectName, "project-name", "p", "", "Compose project name")
	flags.StringVar(&opts.projectDirectory, "project-directory", "", "Project working directory")
	flags.StringArrayVar(&opts.envFiles, "env-file", nil, "Environment file")
	flags.BoolVar(&opts.noColor, "no-color", false, "Disable colored output")

	rootCmd.AddCommand(
		newUpCommand(opts, executeUp),
		newDownCommand(opts, newComposeClient),
		newBuildCommand(opts, newComposeClient),
		newLogsCommand(opts, newComposeClient),
	)

	return rootCmd
}

func newComposeClient() (composeService, error) {
	statePath, err := state.DefaultPath()
	if err != nil {
		return nil, err
	}
	registry, err := newStateServiceRegistry(statePath)
	if err != nil {
		return nil, err
	}
	return compose.NewComposeClient(compose.WithServiceRegistry(registry))
}

func newUpCommand(rootOpts *rootOptions, executeUp func(context.Context, string, compose.ParseOptions, compose.UpOptions, bool) error) *cobra.Command {
	var build bool
	var detach bool
	var noCoreDNS bool

	cmd := &cobra.Command{
		Use:   "up [SERVICE...]",
		Short: "Create and start services",
		RunE: func(cmd *cobra.Command, services []string) error {
			path, err := resolveComposePath(rootOpts.files, rootOpts.projectDirectory)
			if err != nil {
				return err
			}
			parseOpts := compose.ParseOptions{
				ProjectName: rootOpts.projectName,
				WorkingDir:  rootOpts.projectDirectory,
				EnvFiles:    rootOpts.envFiles,
			}
			output := cmd.OutOrStdout()
			fatalDiagnosticRendered := false
			upOpts := compose.UpOptions{
				Services:    services,
				Build:       build,
				Output:      newServiceLogWriter(output, !rootOpts.noColor),
				BuildOutput: newBuildLogWriter(output, !rootOpts.noColor),
				Attach:      !detach,
				OnWarning: func(message string) {
					_ = writeWarning(output, !rootOpts.noColor, message)
				},
				OnFatalWarning: func(message string) {
					fatalDiagnosticRendered = true
					_ = writeFatalWarning(output, !rootOpts.noColor, message)
				},
			}
			err = executeUp(cmd.Context(), path, parseOpts, upOpts, noCoreDNS)
			if fatalDiagnosticRendered && err != nil {
				return markErrorReported(err)
			}
			return err
		},
	}

	cmd.Flags().BoolVar(&build, "build", false, "Build images before starting services")
	cmd.Flags().BoolVarP(&detach, "detach", "d", false, "Run services in the background")
	cmd.Flags().BoolVar(&noCoreDNS, "no-coredns", false, "Do not start ACC CoreDNS; pass service dns entries directly to containers")
	return cmd
}

func newDownCommand(rootOpts *rootOptions, newComposeClient func() (composeService, error)) *cobra.Command {
	var removeOrphans bool
	var volumes bool

	cmd := &cobra.Command{
		Use:   "down [SERVICE...]",
		Short: "Stop and remove services",
		RunE: func(cmd *cobra.Command, services []string) error {
			client, path, parseOpts, err := prepareCommand(rootOpts, newComposeClient)
			if err != nil {
				return err
			}
			output := cmd.OutOrStdout()
			return client.Down(cmd.Context(), path, parseOpts, compose.DownOptions{
				Services:      services,
				RemoveOrphans: removeOrphans,
				Volumes:       volumes,
				OnWarning: func(message string) {
					_ = writeWarning(output, !rootOpts.noColor, message)
				},
			})
		},
	}
	cmd.Flags().BoolVar(&removeOrphans, "remove-orphans", false, "Remove containers for services not declared in the Compose file")
	cmd.Flags().BoolVarP(&volumes, "volumes", "v", false, "Remove named volumes declared by the project")
	return cmd
}

func newBuildCommand(rootOpts *rootOptions, newComposeClient func() (composeService, error)) *cobra.Command {
	return &cobra.Command{
		Use:   "build [SERVICE...]",
		Short: "Build service images",
		RunE: func(cmd *cobra.Command, services []string) error {
			client, path, parseOpts, err := prepareCommand(rootOpts, newComposeClient)
			if err != nil {
				return err
			}
			return client.BuildImages(cmd.Context(), path, parseOpts, compose.BuildOptions{
				Services: services,
				Output:   newBuildLogWriter(cmd.OutOrStdout(), !rootOpts.noColor),
			})
		},
	}
}

func newLogsCommand(rootOpts *rootOptions, newComposeClient func() (composeService, error)) *cobra.Command {
	var follow bool

	cmd := &cobra.Command{
		Use:   "logs [SERVICE...]",
		Short: "View service logs",
		RunE: func(cmd *cobra.Command, services []string) error {
			client, path, parseOpts, err := prepareCommand(rootOpts, newComposeClient)
			if err != nil {
				return err
			}
			return client.Logs(cmd.Context(), path, parseOpts, compose.LogsOptions{
				Services: services,
				Follow:   follow,
				Output:   newServiceLogWriter(cmd.OutOrStdout(), !rootOpts.noColor),
			})
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output")
	return cmd
}

func prepareCommand(rootOpts *rootOptions, newComposeClient func() (composeService, error)) (composeService, string, compose.ParseOptions, error) {
	path, err := resolveComposePath(rootOpts.files, rootOpts.projectDirectory)
	if err != nil {
		return nil, "", compose.ParseOptions{}, err
	}
	client, err := newComposeClient()
	if err != nil {
		return nil, "", compose.ParseOptions{}, err
	}
	return client, path, compose.ParseOptions{
		ProjectName: rootOpts.projectName,
		WorkingDir:  rootOpts.projectDirectory,
		EnvFiles:    rootOpts.envFiles,
	}, nil
}

func resolveComposePath(files []string, projectDirectory string) (string, error) {
	if len(files) > 1 {
		return "", fmt.Errorf("multiple compose files are not supported")
	}
	if len(files) == 1 {
		return files[0], nil
	}

	dir := projectDirectory
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve current directory: %w", err)
		}
	}

	for _, name := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect compose file %q: %w", path, err)
		}
	}

	return "", fmt.Errorf("no compose file found; use --file to specify one")
}
