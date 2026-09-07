package accplugin

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"

	"github.com/coredns/coredns/plugin/test"
	"github.com/miekg/dns"
)

func TestServeDNS(t *testing.T) {
	for _, tcp := range []bool{false, true} {
		for _, tc := range []struct {
			name, source  string
			qtype         uint16
			rcode         int
			addresses     []string
			forward       bool
			customForward bool
		}{
			{"api.", "10.10.0.2", dns.TypeA, dns.RcodeSuccess, []string{"10.10.0.3", "10.10.0.4"}, false, false},
			{"backend-api.", "10.30.0.2", dns.TypeAAAA, dns.RcodeSuccess, []string{"fd00:20::3", "fd00:20::4"}, false, false},
			{"db.", "fd00:20::3", dns.TypeA, dns.RcodeSuccess, []string{"10.10.0.2"}, false, false},
			{"db.", "10.10.0.3", dns.TypeAAAA, dns.RcodeSuccess, nil, false, false},
			{"api.", "10.10.0.2", dns.TypeTXT, dns.RcodeSuccess, nil, false, false},
			{"frontend.", "10.10.0.2", dns.TypeA, dns.RcodeNameError, nil, false, false},
			{"google.com.", "10.10.0.2", dns.TypeA, dns.RcodeSuccess, nil, true, false},
			{"google.com.", "10.10.0.3", dns.TypeA, dns.RcodeSuccess, nil, false, true},
			// Retain forwarding for unknown clients until that policy is agreed.
			{"api.", "10.99.0.2", dns.TypeA, dns.RcodeSuccess, nil, true, false},
		} {
			t.Run(tc.name+dns.TypeToString[tc.qtype]+tc.source+map[bool]string{true: "TCP", false: "UDP"}[tcp], func(t *testing.T) {
				next := &countingHandler{}
				forwarder := &recordingForwarder{}
				h := &ACC{Registry: registryFromState(t, fixtureState()), Next: next, TTL: 60, forwarder: forwarder}
				w := &captureWriter{ResponseWriter: test.ResponseWriter{RemoteIP: tc.source, TCP: tcp}}
				rcode, err := h.ServeDNS(context.Background(), w, question(tc.name, tc.qtype))
				if err != nil || rcode != tc.rcode {
					t.Fatalf("return=(%d,%v)", rcode, err)
				}
				if tc.forward {
					if next.calls != 1 || forwarder.calls != 0 || w.message != nil {
						t.Fatal("forwarding contract violated")
					}
					return
				}
				if tc.customForward {
					if next.calls != 0 || forwarder.calls != 1 || w.message != nil {
						t.Fatal("custom forwarding contract violated")
					}
					if !reflect.DeepEqual(forwarder.nameservers, []string{"1.1.1.1", "9.9.9.9"}) {
						t.Fatalf("nameservers=%v", forwarder.nameservers)
					}
					return
				}
				if next.calls != 0 || forwarder.calls != 0 || w.message == nil || w.message.Rcode != tc.rcode {
					t.Fatal("wrong reply/forwarding")
				}
				var actual []string
				for _, rr := range w.message.Answer {
					if rr.Header().Ttl != 60 {
						t.Fatal("wrong TTL")
					}
					switch record := rr.(type) {
					case *dns.A:
						actual = append(actual, record.A.String())
					case *dns.AAAA:
						actual = append(actual, record.AAAA.String())
					default:
						t.Fatalf("unexpected RR %T", rr)
					}
				}
				if !reflect.DeepEqual(actual, tc.addresses) {
					t.Fatalf("addresses=%v want %v", actual, tc.addresses)
				}
			})
		}
	}
}

func TestServeDNSInvalidRequestsAndWriteFailure(t *testing.T) {
	h := &ACC{Registry: registryFromState(t, fixtureState())}
	update := question("api.", dns.TypeA)
	update.Opcode = dns.OpcodeUpdate
	chaos := question("api.", dns.TypeA)
	chaos.Question[0].Qclass = dns.ClassCHAOS
	multi := question("api.", dns.TypeA)
	multi.Question = append(multi.Question, multi.Question[0])
	for _, tc := range []struct {
		q    *dns.Msg
		want int
	}{
		{nil, dns.RcodeFormatError}, {new(dns.Msg), dns.RcodeFormatError},
		{multi, dns.RcodeFormatError}, {update, dns.RcodeNotImplemented}, {chaos, dns.RcodeRefused},
	} {
		w := &captureWriter{ResponseWriter: test.ResponseWriter{RemoteIP: "10.10.0.2"}}
		code, err := h.ServeDNS(context.Background(), w, tc.q)
		if err != nil || code != tc.want || w.message != nil {
			t.Fatal("error response must be left to CoreDNS")
		}
	}
	failure := errors.New("write failed")
	w := &captureWriter{ResponseWriter: test.ResponseWriter{RemoteIP: "10.10.0.2"}, writeError: failure}
	code, err := h.ServeDNS(context.Background(), w, question("api.", dns.TypeA))
	if code != dns.RcodeServerFailure || !errors.Is(err, failure) {
		t.Fatal("write error not preserved")
	}
}

type captureWriter struct {
	test.ResponseWriter
	message    *dns.Msg
	writeError error
}

func (w *captureWriter) WriteMsg(m *dns.Msg) error {
	if w.writeError != nil {
		return w.writeError
	}
	w.message = m.Copy()
	return nil
}

type countingHandler struct{ calls int }

func (h *countingHandler) Name() string { return "next" }
func (h *countingHandler) ServeDNS(context.Context, dns.ResponseWriter, *dns.Msg) (int, error) {
	h.calls++
	return dns.RcodeSuccess, nil
}

type recordingForwarder struct {
	calls       int
	nameservers []string
}

func (f *recordingForwarder) Forward(_ context.Context, _ dns.ResponseWriter, _ *dns.Msg, nameservers []netip.Addr) (int, error) {
	f.calls++
	for _, nameserver := range nameservers {
		f.nameservers = append(f.nameservers, nameserver.String())
	}
	return dns.RcodeSuccess, nil
}
func question(name string, qtype uint16) *dns.Msg {
	q := new(dns.Msg)
	q.SetQuestion(name, qtype)
	return q
}
