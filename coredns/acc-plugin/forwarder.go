package accplugin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/coredns/coredns/plugin/pkg/log"
	"github.com/miekg/dns"
)

const defaultUpstreamTimeout = 2 * time.Second

var forwardLog = log.NewWithPlugin("acc")

// dnsForwarder forwards unmanaged queries for requesters with an explicit
// nameserver configuration. It deliberately accepts parsed addresses instead
// of Corefile strings so registry validation happens before request handling.
type dnsForwarder interface {
	Forward(context.Context, dns.ResponseWriter, *dns.Msg, []netip.Addr) (int, error)
}

var defaultDNSForwarder dnsForwarder = upstreamForwarder{timeout: defaultUpstreamTimeout}

type upstreamForwarder struct {
	timeout  time.Duration
	exchange dnsExchange
	logInfo  func(string, ...any)
}

type dnsExchange func(context.Context, string, string, *dns.Msg, time.Duration) (*dns.Msg, error)

func (f upstreamForwarder) Forward(ctx context.Context, writer dns.ResponseWriter, request *dns.Msg, nameservers []netip.Addr) (int, error) {
	if request == nil {
		return dns.RcodeServerFailure, errors.New("cannot forward a nil DNS request")
	}
	network := upstreamNetwork(writer)
	timeout := f.timeout
	if timeout <= 0 {
		timeout = defaultUpstreamTimeout
	}
	exchange := f.exchange
	if exchange == nil {
		exchange = exchangeDNS
	}
	logInfo := f.logInfo
	if logInfo == nil {
		logInfo = forwardLog.Infof
	}

	var lastErr error
	for _, nameserver := range nameservers {
		address := net.JoinHostPort(nameserver.String(), "53")
		response, err := exchange(ctx, network, address, request.Copy(), timeout)
		if err == nil && response != nil && response.Truncated && network == "udp" {
			response, err = exchange(ctx, "tcp", address, request.Copy(), timeout)
		}
		if err != nil {
			lastErr = err
			continue
		}
		if response == nil {
			lastErr = errors.New("upstream returned no DNS response")
			continue
		}
		logInfo("forwarded DNS query %q from %s to nameserver %s via %s", queryName(request), requesterAddress(writer), address, network)

		// A compliant upstream preserves the query ID. Set it explicitly so a
		// malformed upstream cannot cause a response to be matched to another
		// client request.
		response.Id = request.Id
		if err := writer.WriteMsg(response); err != nil {
			return dns.RcodeServerFailure, err
		}
		return response.Rcode, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no nameservers configured")
	}
	return dns.RcodeServerFailure, fmt.Errorf("all configured nameservers failed: %w", lastErr)
}

func queryName(request *dns.Msg) string {
	if request == nil || len(request.Question) == 0 {
		return "<unknown>"
	}
	return request.Question[0].Name
}

func requesterAddress(writer dns.ResponseWriter) string {
	if writer == nil || writer.RemoteAddr() == nil {
		return "<unknown>"
	}
	return writer.RemoteAddr().String()
}

func upstreamNetwork(writer dns.ResponseWriter) string {
	if writer != nil && writer.LocalAddr() != nil && writer.LocalAddr().Network() == "tcp" {
		return "tcp"
	}
	return "udp"
}

func exchangeDNS(ctx context.Context, network, address string, request *dns.Msg, timeout time.Duration) (*dns.Msg, error) {
	client := &dns.Client{Net: network, Timeout: timeout}
	response, _, err := client.ExchangeContext(ctx, request, address)
	return response, err
}
