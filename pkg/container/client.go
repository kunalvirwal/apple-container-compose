package container

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type Client struct {
	Bin string

	Container ContainerClient
	System    SystemClient
}

func NewClient() (*Client, error) {
	c := &Client{
		Bin: "container",
	}
	if !c.Available() {
		return nil, ErrContainerNotFound
	}
	c.Container = NewContainerClient(c.run)
	c.System = NewSystemClient(c.run)
	return c, nil
}

func (c *Client) run(args ...string) (string, error) {
	return c.runctx(context.Background(), args...)
}

func (c *Client) runctx(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return string(out), fmt.Errorf("failed to run container: %w\n%s", err, out)
	}
	return string(out), nil
}

func (c *Client) Available() bool {
	_, err := exec.LookPath(c.Bin)
	return err == nil
}

func (c *Client) SystemRunning() bool {
	out, err := c.run("system", "status")
	if err != nil {
		return false
	}
	return !strings.Contains(out, "not running")
}
