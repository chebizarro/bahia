package auth

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestNilRBACReceiverDoesNotPanic(t *testing.T) {
	var r *RBAC // exactly what newTenantRBAC returns with no database
	p := &Principal{Subject: "op", PubKey: "abc", Method: MethodNIP98}
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("nil *RBAC receiver PANICKED: %v", rec)
		}
	}()
	if _, err := r.LoadAuthzContext(context.Background(), p, uuid.New()); err == nil {
		t.Fatalf("nil *RBAC must fail closed, got nil error")
	}
}
