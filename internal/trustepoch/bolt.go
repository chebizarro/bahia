package trustepoch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.etcd.io/bbolt"
)

var acceptedBucket = []byte("bahiaTrustEpochAcceptedV1")

type BoltPersistence struct {
	shared    *sharedBolt
	closeOnce sync.Once
}

type sharedBolt struct {
	db   *bbolt.DB
	refs int
}

var openEpochStates = struct {
	sync.Mutex
	byPath map[string]*sharedBolt
}{byPath: make(map[string]*sharedBolt)}

func OpenBolt(path string) (*BoltPersistence, error) {
	if path == "" {
		return nil, fmt.Errorf("trust epoch persistence path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	openEpochStates.Lock()
	defer openEpochStates.Unlock()
	if shared := openEpochStates.byPath[abs]; shared != nil {
		shared.refs++
		return &BoltPersistence{shared: shared}, nil
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return nil, err
	}
	db, err := bbolt.Open(abs, 0o600, &bbolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, err
	}
	if err := db.Update(func(tx *bbolt.Tx) error { _, err := tx.CreateBucketIfNotExists(acceptedBucket); return err }); err != nil {
		_ = db.Close()
		return nil, err
	}
	shared := &sharedBolt{db: db, refs: 1}
	openEpochStates.byPath[abs] = shared
	return &BoltPersistence{shared: shared}, nil
}

func (b *BoltPersistence) Close() (err error) {
	if b == nil {
		return nil
	}
	b.closeOnce.Do(func() {
		openEpochStates.Lock()
		defer openEpochStates.Unlock()
		b.shared.refs--
		if b.shared.refs == 0 {
			for path, shared := range openEpochStates.byPath {
				if shared == b.shared {
					delete(openEpochStates.byPath, path)
					break
				}
			}
			err = b.shared.db.Close()
		}
	})
	return err
}

func (b *BoltPersistence) Last(ctx context.Context, serviceID string) (Accepted, bool, error) {
	var head Accepted
	var found bool
	err := b.shared.db.View(func(tx *bbolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		raw := tx.Bucket(acceptedBucket).Get([]byte(serviceID))
		if raw == nil {
			return nil
		}
		found = true
		if err := json.Unmarshal(raw, &head); err != nil {
			return fmt.Errorf("decode accepted epoch: %w", err)
		}
		if head.Epoch == 0 || !validHex64(head.EventID) || !validHex64(head.OldPubkey) || !validHex64(head.NewPubkey) {
			return fmt.Errorf("corrupt accepted epoch for service %q", serviceID)
		}
		return nil
	})
	return head, found, err
}

func (b *BoltPersistence) Accept(ctx context.Context, serviceID string, head Accepted) error {
	if serviceID == "" || head.Epoch == 0 || !validHex64(head.EventID) || !validHex64(head.OldPubkey) || !validHex64(head.NewPubkey) {
		return fmt.Errorf("invalid accepted trust epoch")
	}
	return b.shared.db.Update(func(tx *bbolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		bucket := tx.Bucket(acceptedBucket)
		if raw := bucket.Get([]byte(serviceID)); raw != nil {
			var prior Accepted
			if err := json.Unmarshal(raw, &prior); err != nil {
				return fmt.Errorf("decode accepted epoch: %w", err)
			}
			if prior.Epoch == 0 || !validHex64(prior.EventID) || !validHex64(prior.OldPubkey) || !validHex64(prior.NewPubkey) {
				return fmt.Errorf("corrupt accepted trust epoch")
			}
			if head.Epoch < prior.Epoch || (head.Epoch == prior.Epoch && head.EventID != prior.EventID) {
				return ErrRegression
			}
			if head.Epoch == prior.Epoch {
				if head != prior {
					return ErrUnverifiable
				}
				return nil
			}
		}
		raw, err := json.Marshal(head)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(serviceID), raw)
	})
}
