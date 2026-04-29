package middleware

import (
	"encoding/base64"
	"net"
	"net/http"
	"slices"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

// ResolveRegistryPrincipal resolves a registry principal from NIP-98, Basic auth, or anonymous.
func ResolveRegistryPrincipal(r *http.Request, nip98 *auth.NIP98Validator, cfg config.OCIServerConfig) *domain.RegistryPrincipal {
	authz := strings.TrimSpace(r.Header.Get("Authorization"))
	if authz == "" {
		if allowAnonymousPull(r, cfg) {
			return &domain.RegistryPrincipal{Subject: "anonymous", AuthType: "anonymous", Scopes: []string{"repository:*:pull"}}
		}
		return &domain.RegistryPrincipal{Subject: "anonymous", AuthType: "anonymous"}
	}

	lower := strings.ToLower(authz)
	if strings.HasPrefix(lower, "nostr ") && nip98 != nil {
		token := strings.TrimSpace(authz[len("Nostr "):])
		if p, err := nip98.Validate(token, r); err == nil && p != nil {
			if len(cfg.AuthorizedPushPubkeys) > 0 && !slices.Contains(cfg.AuthorizedPushPubkeys, p.PubKey) {
				return &domain.RegistryPrincipal{Subject: "anonymous", AuthType: "anonymous"}
			}
			return &domain.RegistryPrincipal{
				Subject:  p.Subject,
				AuthType: "nip98",
				Pubkey:   p.PubKey,
				Scopes:   []string{"repository:*:pull", "repository:*:push"},
			}
		}
	}

	if strings.HasPrefix(lower, "basic ") {
		raw := strings.TrimSpace(authz[len("Basic "):])
		decoded, err := base64.StdEncoding.DecodeString(raw)
		if err == nil {
			parts := strings.SplitN(string(decoded), ":", 2)
			if len(parts) == 2 && parts[0] != "" {
				username := parts[0]
				password := parts[1]
				for _, acct := range cfg.ServiceAccounts {
					if acct.Username != username {
						continue
					}
					if bcrypt.CompareHashAndPassword([]byte(acct.PasswordHash), []byte(password)) != nil {
						break
					}
					scopes := make([]string, 0, len(acct.RepoPrefixes))
					actions := strings.Join(acct.Permissions, ",")
					for _, prefix := range acct.RepoPrefixes {
						prefix = strings.TrimSpace(prefix)
						if prefix == "" {
							continue
						}
						scopes = append(scopes, "repository:"+strings.TrimSuffix(prefix, "/")+"/*:"+actions)
					}
					return &domain.RegistryPrincipal{
						Subject:        username,
						AuthType:       "basic",
						ServiceAccount: username,
						Scopes:         scopes,
					}
				}
			}
		}
	}

	if allowAnonymousPull(r, cfg) {
		return &domain.RegistryPrincipal{Subject: "anonymous", AuthType: "anonymous", Scopes: []string{"repository:*:pull"}}
	}
	return &domain.RegistryPrincipal{Subject: "anonymous", AuthType: "anonymous"}
}

func allowAnonymousPull(r *http.Request, cfg config.OCIServerConfig) bool {
	if len(cfg.AllowAnonymousPullCIDRs) == 0 {
		return false
	}
	ip := extractClientIP(r, cfg)
	if ip == nil {
		return false
	}
	for _, cidr := range cfg.AllowAnonymousPullCIDRs {
		_, block, err := net.ParseCIDR(strings.TrimSpace(cidr))
		if err == nil && block.Contains(ip) {
			return true
		}
	}
	return false
}

func extractClientIP(r *http.Request, cfg config.OCIServerConfig) net.IP {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	proxyIP := net.ParseIP(host)

	xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if xff != "" && proxyIP != nil && isTrustedProxy(proxyIP, cfg.TrustedProxyCIDRs) {
		parts := strings.Split(xff, ",")
		for _, part := range parts {
			ip := net.ParseIP(strings.TrimSpace(part))
			if ip != nil {
				return ip
			}
		}
	}
	return proxyIP
}

func isTrustedProxy(ip net.IP, cidrs []string) bool {
	if ip == nil || len(cidrs) == 0 {
		return false
	}
	for _, cidr := range cidrs {
		_, block, err := net.ParseCIDR(strings.TrimSpace(cidr))
		if err == nil && block.Contains(ip) {
			return true
		}
	}
	return false
}

// WithRegistryPrincipal sets the resolved registry principal in request context.
func WithRegistryPrincipal(r *http.Request, principal *domain.RegistryPrincipal) *http.Request {
	return r.WithContext(service.WithRegistryPrincipal(r.Context(), principal))
}
