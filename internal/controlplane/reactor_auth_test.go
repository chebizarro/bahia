package controlplane

import "testing"

func TestReactorIsAuthorized(t *testing.T) {
	t.Run("empty allowlist denies all", func(t *testing.T) {
		reactor := NewReactor(Config{}, nil, nil, nil, nil)
		if reactor.isAuthorized("pubkey-1") {
			t.Fatal("expected authorization to fail when allowlist is empty")
		}
	})

	t.Run("configured allowlist permits matching pubkey", func(t *testing.T) {
		reactor := NewReactor(Config{AuthorizedPubkeys: []string{"pubkey-1", "pubkey-2"}}, nil, nil, nil, nil)
		if !reactor.isAuthorized("pubkey-2") {
			t.Fatal("expected configured pubkey to be authorized")
		}
		if reactor.isAuthorized("pubkey-3") {
			t.Fatal("expected non-configured pubkey to be rejected")
		}
	})
}
