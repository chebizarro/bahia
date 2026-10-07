package redact

import (
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

const Address = "[REDACTED_ADDRESS]"

// Address tokens include IPv6 brackets/zones and URL percent escapes. Parsing,
// rather than a regex approximation of IP syntax, decides what is an address.
var addressToken = regexp.MustCompile(`[a-zA-Z0-9_.:%\[\]-]+`)

// NetworkAddresses hides IP literals and host:port endpoints from diagnostic
// text destined for third parties. All address ranges are hidden: public IPs
// can also identify internal interfaces behind NAT or split-horizon routing.
// DNS route names without ports remain useful for identifying the public route.
func NetworkAddresses(text string) string {
	return addressToken.ReplaceAllStringFunc(text, func(token string) string {
		candidate := token
		for {
			if isNetworkAddress(candidate) {
				return Address + token[len(candidate):]
			}
			// Transport errors commonly append a colon, prose a full stop.
			// Try the intact token first so IPv6 addresses ending in :: survive.
			if !strings.HasSuffix(candidate, ":") && !strings.HasSuffix(candidate, ".") && !strings.HasSuffix(candidate, "-") {
				return token
			}
			candidate = candidate[:len(candidate)-1]
		}
	})
}

func isNetworkAddress(token string) bool {
	bare := token
	if strings.HasPrefix(bare, "[") && strings.HasSuffix(bare, "]") {
		bare = bare[1 : len(bare)-1]
	}
	if addr, err := netip.ParseAddr(bare); err == nil && !strings.ContainsAny(addr.Zone(), ":[]") {
		return true
	}
	host, port, err := net.SplitHostPort(token)
	if err != nil || host == "" || port == "" {
		return false
	}
	_, err = strconv.ParseUint(port, 10, 16)
	return err == nil
}
