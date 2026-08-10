package container

import (
	"context"
	"net"
	"strconv"
	"strings"
)

type ContainerClient struct {
	run func(ctx context.Context, args ...string) (string, error)
}

func NewContainerClient(run func(ctx context.Context, args ...string) (string, error)) ContainerClient {
	return ContainerClient{
		run: run,
	}
}

// ListOptions defines the options for listing containers
type ListOptions struct {
	// Display both running and stopped containers
	All bool
	// Return only container IDs
	Quiet bool
}

// List returns a list of containers based on the provided options
func (c *ContainerClient) List(ctx context.Context, opts ListOptions) (string, error) {
	args := []string{"list"}

	// Flag parsing
	// only json output is supported
	args = append(args, "--format", "json")
	if opts.All {
		args = append(args, "--all")
	}

	out, err := c.run(ctx, args...)
	if err != nil {
		if strings.Contains(out, "failed to list containers") {
			return "", ErrSystemNotRunning
		}
	}
	return out, err
}

// CreateOptions defines the options for creating a container
type CreateOptions struct {
	// Name of the container to create
	Name string
	// Number of CPUs to allocate to the container
	CPUs uint
	// Amount of memory (1MiByte granularity), with optional K, M, G, T, or P suffix
	Memory string
	// Remove the container after it exits
	Rm bool
	// Port mappings
	Publish []PortMapping
	// Container init Process arguments, if any
	Arguments []string

	// [TODO]: add env varable support
}

// PortMapping defines a mapping from a host port to a container port as taken by the container cli
type PortMapping struct {
	HostIP        string
	HostPort      uint16
	ContainerPort uint16
	Protocol      Protocol // "tcp" or "udp"
}

// Protocol types accepted by the container cli defined in the container package: TCP and UDP
type Protocol string

const (
	TCP Protocol = "tcp"
	UDP Protocol = "udp"
)

// Run creates and starts a new container based on the provided image and options. Returns true if the container was successfully created and started, or an error if the operation fails.
func (c *ContainerClient) Run(ctx context.Context, image string, opts CreateOptions) (bool, error) {
	args := []string{"run"}

	if opts.Name != "" {
		args = append(args, "--name", opts.Name)
	}
	if opts.CPUs > 0 {
		args = append(args, "--cpus", strconv.Itoa(int(opts.CPUs)))
	}
	if opts.Memory != "" {
		size := opts.Memory[:len(opts.Memory)-1]
		suffix := opts.Memory[len(opts.Memory)-1]
		if suffix != 'K' && suffix != 'M' && suffix != 'G' && suffix != 'T' && suffix != 'P' {
			return false, ErrInvalidOptions
		}
		_, err := strconv.ParseUint(size, 10, 0)
		if err != nil {
			return false, ErrInvalidOptions
		}
		args = append(args, "--memory", opts.Memory)
	}
	if opts.Rm {
		args = append(args, "--rm")
	}
	if opts.Publish != nil {
		for _, mapping := range opts.Publish {
			portMapping := ""
			if mapping.HostIP != "" {
				ip := net.ParseIP(mapping.HostIP) // Validate the IP address
				if ip == nil || ip.To4() == nil {
					return false, ErrInvalidOptions
				}
				portMapping += ip.String() + ":"
			}
			portMapping += strconv.Itoa(int(mapping.HostPort)) + ":" + strconv.Itoa(int(mapping.ContainerPort))
			if mapping.Protocol != "" {
				portMapping += "/" + string(mapping.Protocol)
			}
			args = append(args, "-p", portMapping)
		}
	}
	// To run the container detached
	args = append(args, "-d")
	args = append(args, image)
	args = append(args, opts.Arguments...)

	out, err := c.run(ctx, args...)
	if err != nil {
		if strings.Contains(out, "XPC connection error") {
			return false, ErrSystemNotRunning
		} else if strings.Contains(out, "already exists") {
			return false, ErrContainerNameExists
		}
	}
	return true, err
}

// StopOptions defines the options for stopping containers
type StopOptions struct {
	// Stop all running containers
	All bool
	// List of container IDs or names to stop, should be empty if All flag is true
	IDs []string
	// Seconds to wait before killing the containers (default: 5)
	Time uint
	// Signal to send to the containers (default: SIGTERM)
	Signal Signal
}

// Signal defines the signal types accepted by the container cli defined under container package
type Signal string

const (
	SIGTERM Signal = "SIGTERM"
	SIGKILL Signal = "SIGKILL"
	SIGINT  Signal = "SIGINT"
	SIGQUIT Signal = "SIGQUIT"
	SIGUSR1 Signal = "SIGUSR1"
	SIGUSR2 Signal = "SIGUSR2"
)

// Stop stops one or more running containers based on the provided options. Returns the ID of the stopped container(s) and an error if the operation fails.
func (c *ContainerClient) Stop(ctx context.Context, opts StopOptions) (string, error) {
	args := []string{}
	if opts.Signal != "" {
		args = append(args, "--signal", string(opts.Signal))
	}
	if opts.Time > 0 {
		args = append(args, "--time", strconv.Itoa(int(opts.Time)))
	}
	if opts.All {
		if len(opts.IDs) > 0 {
			return "", ErrInvalidOptions
		}
		out, err := c.run(ctx, append([]string{"stop", "--all"}, args...)...)
		if err != nil {
			return out, err
		}
		return out, nil
	}
	if len(opts.IDs) == 0 {
		return "", ErrInvalidOptions
	}
	args = append(args, opts.IDs...)
	args = append([]string{"stop"}, args...)
	out, err := c.run(ctx, args...)
	if err != nil {
		return out, err
	}
	return out, nil
}

type DeleteOptions struct {
	// Delete all containers
	All bool
	// List of container IDs or names to delete, should be empty if All flag is true
	IDs []string
	// Force deletion of running containers
	Force bool
}

// Delete deletes one or more containers based on the provided options. Returns the ID of the deleted container(s) and an error if the operation fails.
func (c *ContainerClient) Delete(ctx context.Context, opts DeleteOptions) (string, error) {
	args := []string{}
	if opts.Force {
		args = append(args, "--force")
	}
	if opts.All {
		if len(opts.IDs) > 0 {
			return "", ErrInvalidOptions
		}
		out, err := c.run(ctx, append([]string{"delete", "--all"}, args...)...)
		if err != nil {
			return out, err
		}
		return out, nil
	}
	if len(opts.IDs) == 0 {
		return "", ErrInvalidOptions
	}
	args = append(args, opts.IDs...)
	args = append([]string{"delete"}, args...)
	out, err := c.run(ctx, args...)
	if err != nil {
		return out, err
	}
	return out, nil
}
