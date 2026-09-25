package redact

import "testing"

func TestNetworkAddresses(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"dial tcp 192.168.40.10:443: connection refused", "dial tcp " + Address + ": connection refused"},
		{"read tcp 10.1.2.3:55311->172.16.1.2:443: timeout", "read tcp " + Address + "->" + Address + ": timeout"},
		{"dial tcp [fd00::1234]:443: connection refused", "dial tcp " + Address + ": connection refused"},
		{"dial tcp [fe80::1%eth0]:443: no route to host", "dial tcp " + Address + ": no route to host"},
		{"IPs: 127.0.0.1, 100.64.0.1, 169.254.1.2, ::1, ::, 8.8.8.8.", "IPs: " + Address + ", " + Address + ", " + Address + ", " + Address + ", " + Address + ", " + Address + "."},
		{"IPv6: 2001:db8::1234 ::ffff:192.168.40.10", "IPv6: " + Address + " " + Address},
		{"lookup git.example.test on dns.internal:53: no such host", "lookup git.example.test on " + Address + ": no such host"},
		{"Get \"https://origin.internal:8443/healthz\": EOF", "Get \"https://" + Address + "/healthz\": EOF"},
		{"GET https://git.example.test/healthz: HTTP 502 in 12ms", "GET https://git.example.test/healthz: HTTP 502 in 12ms"},
		{"certificate valid for 10.0.0.1, not git.example.test", "certificate valid for " + Address + ", not git.example.test"},
	} {
		t.Run(test.input, func(t *testing.T) {
			if got := NetworkAddresses(test.input); got != test.want {
				t.Errorf("NetworkAddresses() = %q, want %q", got, test.want)
			}
			if got := NetworkAddresses(test.want); got != test.want {
				t.Errorf("redaction must be idempotent, got %q", got)
			}
		})
	}
}
