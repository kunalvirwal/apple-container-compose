package accplugin

import (
	"context"
	"net"
	"net/netip"
	"strings"

	"github.com/coredns/coredns/plugin"
	"github.com/miekg/dns"
)

const defaultTTL uint32 = 120

// ACC is the CoreDNS adapter around the file-backed ACC registry.
type ACC struct {
	Next     plugin.Handler
	Registry *FileRegistry
	TTL      uint32
}

// Name implements plugin.Handler.
func (a *ACC) Name() string { return "acc" }

// ServeDNS resolves ACC-managed names according to the requester's network
// memberships. Unknown names continue through the normal CoreDNS chain.
func (a *ACC) ServeDNS(ctx context.Context, w dns.ResponseWriter, r *dns.Msg) (int, error) {
	if r == nil || len(r.Question) != 1 {
		return dns.RcodeFormatError, nil
	}
	if r.Opcode != dns.OpcodeQuery {
		return dns.RcodeNotImplemented, nil
	}
	if r.Question[0].Qclass != dns.ClassINET {
		return dns.RcodeRefused, nil
	}
	if a.Registry == nil {
		return dns.RcodeServerFailure, nil
	}
	sourceIP, ok := remoteIP(w.RemoteAddr())
	if !ok {
		return plugin.NextOrFailure(a.Name(), a.Next, ctx, w, r)
	}
	question := r.Question[0]
	resolution := a.Registry.Resolve(sourceIP, question.Name)
	switch resolution.Kind {
	case ResolutionUnmanaged:
		return plugin.NextOrFailure(a.Name(), a.Next, ctx, w, r)
	case ResolutionHidden:
		return writeReply(w, r, dns.RcodeNameError, nil)
	case ResolutionUnknownRequester:
		// Policy pending discussion: preserve the original forwarding behavior.
		// ACC has no memberships for this client, so cannot return internal IPs.
		return plugin.NextOrFailure(a.Name(), a.Next, ctx, w, r)
	case ResolutionAnswer:
		return writeReply(w, r, dns.RcodeSuccess, answerRecords(question, resolution.Addresses, a.TTL))
	default:
		return plugin.NextOrFailure(a.Name(), a.Next, ctx, w, r)
	}
}

func remoteIP(address net.Addr) (netip.Addr, bool) {
	if address == nil {
		return netip.Addr{}, false
	}
	host, _, err := net.SplitHostPort(address.String())
	if err != nil {
		return netip.Addr{}, false
	}
	host, _, _ = strings.Cut(host, "%")
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return ip.Unmap(), true
}

func answerRecords(question dns.Question, addresses []netip.Addr, ttl uint32) []dns.RR {
	if ttl == 0 {
		ttl = defaultTTL
	}
	records := make([]dns.RR, 0, len(addresses))
	for _, address := range addresses {
		header := dns.RR_Header{Name: question.Name, Rrtype: question.Qtype, Class: dns.ClassINET, Ttl: ttl}
		switch {
		case question.Qtype == dns.TypeA && address.Is4():
			records = append(records, &dns.A{Hdr: header, A: net.IP(address.AsSlice())})
		case question.Qtype == dns.TypeAAAA && address.Is6():
			records = append(records, &dns.AAAA{Hdr: header, AAAA: net.IP(address.AsSlice())})
		}
	}
	return records
}

func writeReply(w dns.ResponseWriter, request *dns.Msg, rcode int, records []dns.RR) (int, error) {
	reply := new(dns.Msg)
	reply.SetReply(request)
	reply.Authoritative = true
	reply.RecursionAvailable = false
	reply.Rcode = rcode
	reply.Answer = records
	if err := w.WriteMsg(reply); err != nil {
		return dns.RcodeServerFailure, err
	}
	// Only NOERROR/NXDOMAIN are written here. Error rcodes must be returned
	// without writing so CoreDNS emits exactly one failure response.
	return rcode, nil
}
