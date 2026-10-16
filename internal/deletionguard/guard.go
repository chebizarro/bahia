// Package deletionguard validates inventory-scoped NIP-09 deletion requests.
package deletionguard

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
)

const MaxBatchSize = 500

// Check allows only kind-5, empty-content, e/k-only requests for inventoried IDs.
// An optional allowedKinds set narrows callers such as the leak-delete tool.
func Check(ev nostr.Event, author nostr.PubKey, targets map[nostr.ID]nostr.Kind, allowedKinds map[nostr.Kind]struct{}) error {
	if ev.Kind != nostr.KindDeletion {
		return errors.New("only kind 5 is allowed")
	}
	if ev.PubKey != author {
		return errors.New("only the leaked identity may request deletion")
	}
	if ev.Content != "" {
		return errors.New("refusing deletion with content")
	}
	referenced := map[nostr.ID]struct{}{}
	actualKinds := map[nostr.Kind]struct{}{}
	declaredKinds := map[nostr.Kind]struct{}{}
	for _, tag := range ev.Tags {
		if len(tag) != 2 {
			return fmt.Errorf("refusing deletion with a %d-element tag", len(tag))
		}
		if strings.Contains(strings.ToLower(strings.Join(tag, "")), "nsec1") {
			return errors.New("refusing deletion: a tag contains an nsec")
		}
		switch tag[0] {
		case "e":
			id, err := nostr.IDFromHex(tag[1])
			if err != nil || id.Hex() != tag[1] {
				return errors.New("e tag is not a lowercase 64-hex event id")
			}
			kind, ok := targets[id]
			if !ok {
				return errors.New("e tag names an event no scan found")
			}
			if _, ok := referenced[id]; ok {
				return errors.New("e tag repeats")
			}
			if allowedKinds != nil {
				if _, ok := allowedKinds[kind]; !ok {
					return errors.New("deletion kind outside allowed set")
				}
			}
			referenced[id] = struct{}{}
			actualKinds[kind] = struct{}{}
		case "k":
			number, err := strconv.Atoi(tag[1])
			if err != nil || strconv.Itoa(number) != tag[1] {
				return errors.New("invalid deletion kind tag")
			}
			kind := nostr.Kind(number)
			if allowedKinds != nil {
				if _, ok := allowedKinds[kind]; !ok {
					return errors.New("k tag is not a leaked status kind")
				}
			}
			if _, ok := declaredKinds[kind]; ok {
				return errors.New("k tag repeats")
			}
			declaredKinds[kind] = struct{}{}
		default:
			return fmt.Errorf("refusing deletion with a %q tag: only e and k tags are allowed", tag[0])
		}
	}
	if len(referenced) == 0 {
		return errors.New("refusing deletion without e tags")
	}
	if len(referenced) > MaxBatchSize {
		return errors.New("too many deletion e tags")
	}
	if len(actualKinds) != len(declaredKinds) {
		return errors.New("k tags must list exactly the referenced kinds")
	}
	for kind := range actualKinds {
		if _, ok := declaredKinds[kind]; !ok {
			return errors.New("deletion kind mismatch")
		}
	}
	return nil
}

func CheckSigned(ev nostr.Event, author nostr.PubKey, targets map[nostr.ID]nostr.Kind, allowedKinds map[nostr.Kind]struct{}) error {
	if err := Check(ev, author, targets, allowedKinds); err != nil {
		return err
	}
	if !ev.CheckID() || !ev.VerifySignature() {
		return errors.New("signed deletion does not verify")
	}
	return nil
}
