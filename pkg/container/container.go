package container

import "strings"

type ContainerClient struct {
	run func(args ...string) (string, error)
}

func NewContainerClient(run func(args ...string) (string, error)) ContainerClient {
	return ContainerClient{
		run: run,
	}
}

func (c *ContainerClient) List() ([]string, error) {
	out, err := c.run("list")
	if err != nil {
		if strings.Contains(out, "failed to list containers") {
			return nil, ErrSystemNotRunning
		}
	}
	return nil, err
}
