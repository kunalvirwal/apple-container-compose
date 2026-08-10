package container

import (
	"context"
	"strings"
)

type SystemClient struct {
	run func(ctx context.Context, args ...string) (string, error)
}

func NewSystemClient(run func(ctx context.Context, args ...string) (string, error)) SystemClient {
	return SystemClient{
		run: run,
	}
}

func (s *SystemClient) Status(ctx context.Context) (string, error) {
	out, err := s.run(ctx, "system", "status")
	if err != nil {
		if strings.Contains(out, "not running") {
			return out, ErrSystemNotRunning
		}
	}
	return out, err
}

func (s *SystemClient) Start(ctx context.Context) (bool, error) {
	out, err := s.run(ctx, "system", "start")
	if err != nil {
		return false, err
	}
	if strings.Contains(out, "API server is running") {
		return true, nil
	}
	return false, err
}

func (s *SystemClient) Stop(ctx context.Context) (bool, error) {
	out, err := s.run(ctx, "system", "stop")
	if err != nil {
		return false, err
	}
	if strings.Contains(out, "stopping service") || strings.Contains(out, "skipping bootout") {
		return true, nil
	}
	return false, err
}
