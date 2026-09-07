// Package accplugin implements ACC's CoreDNS service-discovery plugin.
package accplugin

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/coredns/caddy"
	"github.com/coredns/coredns/core/dnsserver"
	"github.com/coredns/coredns/plugin"
)

const defaultPollInterval = 5 * time.Second

// init registers the acc Corefile directive.
func init() { plugin.Register("acc", setup) }

// setup configures an ACC registry backed by a JSON state file. The directive
// accepts exactly one argument: the absolute path to the mounted state file.
func setup(c *caddy.Controller) error {
	path, err := parseStatePath(c)
	if err != nil {
		return plugin.Error("acc", err)
	}

	registry, err := NewFileRegistry(path, defaultPollInterval)
	if err != nil {
		return plugin.Error("acc", err)
	}

	c.OnStartup(func() error {
		registry.Start()
		return nil
	})
	c.OnShutdown(func() error {
		registry.Close()
		return nil
	})

	// Add the plugin to CoreDNS's handler chain. Its compiled plugin ordering
	// must put acc before forward so unknown external names can continue on.
	// Do not put a shared cache before acc: ordinary CoreDNS cache keys omit
	// requester/network identity and can return another network's answer.
	dnsserver.GetConfig(c).AddPlugin(func(next plugin.Handler) plugin.Handler {
		return &ACC{Next: next, Registry: registry, TTL: defaultTTL}
	})

	return nil
}

func parseStatePath(c *caddy.Controller) (string, error) {
	if !c.Next() {
		return "", fmt.Errorf("state file path is required")
	}
	args := c.RemainingArgs()
	if len(args) != 1 {
		return "", c.ArgErr()
	}
	if !filepath.IsAbs(args[0]) {
		return "", fmt.Errorf("state file path must be absolute")
	}
	if c.NextArg() {
		return "", fmt.Errorf("acc does not accept a configuration block")
	}
	return args[0], nil
}
