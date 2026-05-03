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

func TestReactorIsAuthorizedForAdoption(t *testing.T) {
	reactor := NewReactor(Config{
		AuthorizedPubkeys:         []string{"global-operator"},
		AdoptionAuthorizedPubkeys: []string{"adoption-operator"},
	}, nil, nil, nil, nil)

	if !reactor.isAuthorizedFor("global-operator", operatorScopeAdoption) {
		t.Fatal("expected global operator to be authorized for adoption")
	}
	if !reactor.isAuthorizedFor("adoption-operator", operatorScopeAdoption) {
		t.Fatal("expected adoption operator to be authorized for adoption")
	}
	if reactor.isAuthorizedFor("adoption-operator", operatorScopeDefault) {
		t.Fatal("expected adoption-scoped operator to be rejected for default scope")
	}
	if reactor.isAuthorizedFor("unknown", operatorScopeAdoption) {
		t.Fatal("expected unknown operator to be rejected for adoption")
	}
}
