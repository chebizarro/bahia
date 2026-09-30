package keyer

import (
	"context"
	"fmt"

	"fiatjaf.com/nostr"
)

var (
	_ nostr.User   = (*ReadOnlyUser)(nil)
	_ nostr.Signer = (*ReadOnlySigner)(nil)
)

// ReadOnlyUser is a nostr.User that has this public key
type ReadOnlyUser struct {
	pk nostr.PubKey
}

func NewReadOnlyUser(pk nostr.PubKey) ReadOnlyUser {
	return ReadOnlyUser{pk}
}

// GetPublicKey returns the public key associated with this signer.
func (ros ReadOnlyUser) GetPublicKey(context.Context) (nostr.PubKey, error) {
	return ros.pk, nil
}

// ReadOnlySigner is like a ReadOnlyUser, but has a fake GetPublicKey method that doesn't work.
type ReadOnlySigner struct {
	pk nostr.PubKey
}

func NewReadOnlySigner(pk nostr.PubKey) ReadOnlySigner {
	return ReadOnlySigner{pk}
}

// SignEvent returns an error.
func (ros ReadOnlySigner) SignEvent(context.Context, *nostr.Event) error {
	return fmt.Errorf("read-only, we don't have the secret key, cannot sign")
}

// GetPublicKey returns the public key associated with this signer.
func (ros ReadOnlySigner) GetPublicKey(context.Context) (nostr.PubKey, error) {
	return ros.pk, nil
}
