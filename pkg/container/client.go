package container

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

type Client struct {
	Bin string

	Container ContainerClient
	System    SystemClient
	Images    ImageClient
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
	c.Images = NewImageClient(c.run, c.runStreaming)
	return c, nil
}

func (c *Client) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return string(out), fmt.Errorf("failed to run container command: %w\n%s", err, out)
	}
	return string(out), nil
}

type serializedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *serializedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

func (c *Client) runStreaming(ctx context.Context, out io.Writer, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.Bin, args...)

	buf := &bytes.Buffer{}
	writers := []io.Writer{buf}
	if out != nil {
		writers = append([]io.Writer{out}, writers...)
	}
	writer := &serializedWriter{w: io.MultiWriter(writers...)}

	cmd.Stdout = writer
	cmd.Stderr = writer

	err := cmd.Run()
	outstr := buf.String()
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return outstr, fmt.Errorf("failed to run container command: %w\n%s", err, outstr)
	}

	return outstr, nil
}

func (c *Client) Available() bool {
	_, err := exec.LookPath(c.Bin)
	return err == nil
}

func (c *Client) SystemRunning(ctx context.Context) bool {
	out, err := c.run(ctx, "system", "status")
	if err != nil {
		return false
	}
	return !strings.Contains(out, "not running")
}
