package secretstore

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestValidateRef(t *testing.T) {
	valid := domain.SecretCredentialRef{Store: BackendSignet, CredentialID: "cred_" + strings.Repeat("a", 64), Version: 1}
	if err := ValidateRef(valid); err != nil {
		t.Fatalf("valid ref rejected: %v", err)
	}
	for name, ref := range map[string]domain.SecretCredentialRef{
		"other store":   {Store: "vault", CredentialID: valid.CredentialID, Version: 1},
		"empty store":   {CredentialID: valid.CredentialID, Version: 1},
		"empty id":      {Store: BackendSignet, Version: 1},
		"padded id":     {Store: BackendSignet, CredentialID: " " + valid.CredentialID, Version: 1},
		"embedded tab":  {Store: BackendSignet, CredentialID: "cred\tx", Version: 1},
		"oversized id":  {Store: BackendSignet, CredentialID: strings.Repeat("c", 129), Version: 1},
		"zero version":  {Store: BackendSignet, CredentialID: valid.CredentialID},
		"negative vers": {Store: BackendSignet, CredentialID: valid.CredentialID, Version: -2},
	} {
		if err := ValidateRef(ref); !errors.Is(err, ErrInvalidRef) {
			t.Errorf("%s: err = %v, want ErrInvalidRef", name, err)
		}
	}
}

func TestValueZeroizeOverwritesAndRedacts(t *testing.T) {
	buf := []byte("hunter2-canary")
	lease := time.Unix(1_800_000_000, 0)
	v := NewValue(buf, lease)
	if got := string(v.Bytes()); got != "hunter2-canary" {
		t.Fatalf("Bytes = %q", got)
	}
	if !v.LeaseExpiresAt().Equal(lease) {
		t.Fatalf("LeaseExpiresAt = %v", v.LeaseExpiresAt())
	}
	for _, rendered := range []string{fmt.Sprint(v), fmt.Sprintf("%v %+v %#v %s", v, v, v, v)} {
		if strings.Contains(rendered, "canary") {
			t.Fatalf("formatting leaked the value: %q", rendered)
		}
	}
	v.Zeroize()
	for i, b := range buf {
		if b != 0 {
			t.Fatalf("byte %d not zeroized", i)
		}
	}
	if v.Bytes() != nil {
		t.Fatal("Bytes after Zeroize must be nil")
	}
	v.Zeroize() // idempotent
}

func TestScopeAdmitsOnlyItsLabelNamespace(t *testing.T) {
	org, svc := uuid.New(), uuid.New()
	scope := Scope{OrgID: org, ServiceID: svc}
	if got := scope.LabelPrefix(); got != org.String()+"/"+svc.String()+"/" {
		t.Fatalf("LabelPrefix = %q", got)
	}
	if !scope.Admits(org.String() + "/" + svc.String() + "/DB_PASSWORD") {
		t.Fatal("the scope's own label refused")
	}
	for name, label := range map[string]string{
		"bare prefix":      scope.LabelPrefix(),
		"other service":    org.String() + "/" + uuid.NewString() + "/DB_PASSWORD",
		"other org":        uuid.NewString() + "/" + svc.String() + "/DB_PASSWORD",
		"unscoped":         "DB_PASSWORD",
		"prefix lookalike": org.String() + "/" + svc.String() + "x/DB_PASSWORD",
		"empty":            "",
	} {
		if scope.Admits(label) {
			t.Errorf("%s: admitted %q", name, label)
		}
	}
	if (Scope{}).Admits("00000000-0000-0000-0000-000000000000/00000000-0000-0000-0000-000000000000/x") {
		t.Fatal("an empty scope admits nothing")
	}
}
