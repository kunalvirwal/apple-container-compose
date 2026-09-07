package accplugin

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coredns/coredns/plugin/test"
	"github.com/miekg/dns"
)

func TestUpstreamForwarder(t *testing.T) {
	t.Run("uses configured nameservers in order", func(t *testing.T) {
		var calls []string
		var logs []string
		forwarder := upstreamForwarder{
			timeout: time.Second,
			logInfo: func(format string, args ...any) {
				logs = append(logs, fmt.Sprintf(format, args...))
			},
			exchange: func(_ context.Context, network, address string, request *dns.Msg, _ time.Duration) (*dns.Msg, error) {
				calls = append(calls, network+" "+address)
				if address == "1.1.1.1:53" {
					return nil, errors.New("unreachable")
				}
				response := new(dns.Msg)
				response.SetReply(request)
				response.Rcode = dns.RcodeNameError
				response.Id = 99
				return response, nil
			},
		}
		request := question("example.com.", dns.TypeA)
		request.Id = 42
		writer := &captureWriter{ResponseWriter: test.ResponseWriter{RemoteIP: "10.10.0.2"}}
		code, err := forwarder.Forward(context.Background(), writer, request, []netip.Addr{
			netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("9.9.9.9"),
		})
		if err != nil || code != dns.RcodeNameError {
			t.Fatalf("forward result=(%d, %v)", code, err)
		}
		if !reflect.DeepEqual(calls, []string{"udp 1.1.1.1:53", "udp 9.9.9.9:53"}) {
			t.Fatalf("calls=%v", calls)
		}
		if writer.message == nil || writer.message.Id != request.Id || writer.message.Rcode != dns.RcodeNameError {
			t.Fatalf("unexpected reply: %#v", writer.message)
		}
		if len(logs) != 1 || !strings.Contains(logs[0], `"example.com."`) || !strings.Contains(logs[0], "9.9.9.9:53") {
			t.Fatalf("success log=%v", logs)
		}
	})

	t.Run("retries truncated UDP response over TCP", func(t *testing.T) {
		var calls []string
		forwarder := upstreamForwarder{
			exchange: func(_ context.Context, network, address string, request *dns.Msg, _ time.Duration) (*dns.Msg, error) {
				calls = append(calls, network+" "+address)
				response := new(dns.Msg)
				response.SetReply(request)
				response.Truncated = network == "udp"
				return response, nil
			},
		}
		writer := &captureWriter{ResponseWriter: test.ResponseWriter{RemoteIP: "10.10.0.2"}}
		code, err := forwarder.Forward(context.Background(), writer, question("example.com.", dns.TypeA), []netip.Addr{netip.MustParseAddr("1.1.1.1")})
		if err != nil || code != dns.RcodeSuccess {
			t.Fatalf("forward result=(%d, %v)", code, err)
		}
		if !reflect.DeepEqual(calls, []string{"udp 1.1.1.1:53", "tcp 1.1.1.1:53"}) {
			t.Fatalf("calls=%v", calls)
		}
		if writer.message == nil || writer.message.Truncated {
			t.Fatal("truncated UDP response was returned instead of TCP response")
		}
	})

	t.Run("returns servfail after every upstream fails", func(t *testing.T) {
		errUnavailable := errors.New("unavailable")
		forwarder := upstreamForwarder{exchange: func(context.Context, string, string, *dns.Msg, time.Duration) (*dns.Msg, error) {
			return nil, errUnavailable
		}}
		writer := &captureWriter{ResponseWriter: test.ResponseWriter{RemoteIP: "10.10.0.2"}}
		code, err := forwarder.Forward(context.Background(), writer, question("example.com.", dns.TypeA), []netip.Addr{netip.MustParseAddr("1.1.1.1")})
		if code != dns.RcodeServerFailure || !errors.Is(err, errUnavailable) || writer.message != nil {
			t.Fatalf("forward result=(%d, %v, %#v)", code, err, writer.message)
		}
	})
}
