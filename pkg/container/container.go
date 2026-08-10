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

type ListOptions struct {
	// Display both running and stopped containers
	All bool
	// Return only container IDs
	Quiet bool
}

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

type PortMapping struct {
	HostIP        string
	HostPort      uint16
	ContainerPort uint16
	Protocol      Protocol // "tcp" or "udp"
}

type Protocol string

const (
	TCP Protocol = "tcp"
	UDP Protocol = "udp"
)

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
