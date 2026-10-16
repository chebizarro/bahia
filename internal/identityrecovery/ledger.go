package identityrecovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"fiatjaf.com/nostr"
	"go.etcd.io/bbolt"
)

var stateBucket = []byte("recovery-state-v1")
var stateKey = []byte("incident")
var rotationBucket = []byte("rotation-work-v1")

type Ledger struct{ db *bbolt.DB }

func OpenLedger(path, daemonStore string) (*Ledger, error) {
	if path == "" {
		return nil, errors.New("recovery ledger path required")
	}
	a, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	b, err := filepath.Abs(daemonStore)
	if err != nil {
		return nil, err
	}
	if a == b {
		return nil, errors.New("recovery ledger must not share the daemon store file")
	}
	if err = os.MkdirAll(filepath.Dir(a), 0o700); err != nil {
		return nil, err
	}
	db, err := bbolt.Open(a, 0o600, &bbolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open recovery ledger: %w", err)
	}
	if err = db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(stateBucket)
		if err != nil {
			return err
		}
		_, err = tx.CreateBucketIfNotExists(rotationBucket)
		return err
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Ledger{db: db}, nil
}
func (l *Ledger) Close() error { return l.db.Close() }
func (l *Ledger) Load() (State, error) {
	var s State
	err := l.db.View(func(tx *bbolt.Tx) error {
		v := tx.Bucket(stateBucket).Get(stateKey)
		if v == nil {
			return nil
		}
		return json.Unmarshal(v, &s)
	})
	if s.Records == nil {
		s.Records = map[string]nostr.Event{}
	}
	return s, err
}
func (l *Ledger) Save(s State) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return l.db.Update(func(tx *bbolt.Tx) error { return tx.Bucket(stateBucket).Put(stateKey, raw) })
}

// LoadStateReadOnly inspects an existing ledger without creating a file or bucket.
func LoadStateReadOnly(path string) (State, error) {
	var s State
	s.Records = map[string]nostr.Event{}
	if path == "" {
		return s, errors.New("recovery ledger path required")
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return s, nil
	} else if err != nil {
		return s, err
	}
	db, err := bbolt.Open(path, 0o600, &bbolt.Options{ReadOnly: true, Timeout: 2 * time.Second})
	if err != nil {
		return s, err
	}
	defer func() { _ = db.Close() }()
	err = db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(stateBucket)
		if bucket == nil {
			return errors.New("recovery ledger bucket missing")
		}
		raw := bucket.Get(stateKey)
		if raw == nil {
			return nil
		}
		return json.Unmarshal(raw, &s)
	})
	return s, err
}

func (l *Ledger) RotationItems() (map[string]RotationWorkItem, error) {
	out := map[string]RotationWorkItem{}
	err := l.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(rotationBucket)
		if bucket == nil {
			return errors.New("rotation ledger bucket missing")
		}
		return bucket.ForEach(func(k, v []byte) error {
			var item RotationWorkItem
			if err := json.Unmarshal(v, &item); err != nil {
				return err
			}
			if item.Ref.ID != string(k) {
				return errors.New("rotation work item id mismatch")
			}
			out[string(k)] = item
			return nil
		})
	})
	return out, err
}
func (l *Ledger) SaveRotationItem(item RotationWorkItem) error {
	if item.Ref.ID == "" {
		return errors.New("rotation work item id required")
	}
	raw, err := json.Marshal(item)
	if err != nil {
		return err
	}
	return l.db.Update(func(tx *bbolt.Tx) error { return tx.Bucket(rotationBucket).Put([]byte(item.Ref.ID), raw) })
}
