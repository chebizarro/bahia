package localstore

import (
	"bytes"
	"fmt"

	"go.etcd.io/bbolt"
)

// Control records live beside the durable outbound events, not in the
// rebuildable inbound event cache. They are the daemon's local desired-state
// source when PostgreSQL is absent; canonical relay events are published for
// every change and can be used to rebuild this materialization.
var controlRecordsBucket = []byte("bahiaControlRecords")

func controlRecordKey(family, id string) []byte { return []byte(family + "\x00" + id) }

func (o *Outbox) PutControlRecord(family, id string, value []byte) error {
	if o == nil || o.shared == nil || family == "" || id == "" {
		return fmt.Errorf("control record store and key are required")
	}
	if o.shared.readOnly {
		return ErrReadOnly
	}
	return o.shared.db.Update(func(tx *bbolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(controlRecordsBucket)
		if err != nil {
			return err
		}
		return bucket.Put(controlRecordKey(family, id), value)
	})
}

func (o *Outbox) GetControlRecord(family, id string) ([]byte, error) {
	if o == nil || o.shared == nil || family == "" || id == "" {
		return nil, fmt.Errorf("control record store and key are required")
	}
	var value []byte
	err := o.shared.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(controlRecordsBucket)
		if bucket != nil {
			value = bytes.Clone(bucket.Get(controlRecordKey(family, id)))
		}
		return nil
	})
	return value, err
}

func (o *Outbox) ListControlRecords(family string) ([][]byte, error) {
	if o == nil || o.shared == nil || family == "" {
		return nil, fmt.Errorf("control record store and family are required")
	}
	var values [][]byte
	prefix := []byte(family + "\x00")
	err := o.shared.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(controlRecordsBucket)
		if bucket == nil {
			return nil
		}
		cursor := bucket.Cursor()
		for key, value := cursor.Seek(prefix); bytes.HasPrefix(key, prefix); key, value = cursor.Next() {
			values = append(values, bytes.Clone(value))
		}
		return nil
	})
	return values, err
}

func (o *Outbox) DeleteControlRecord(family, id string) (bool, error) {
	if o == nil || o.shared == nil || family == "" || id == "" {
		return false, fmt.Errorf("control record store and key are required")
	}
	if o.shared.readOnly {
		return false, ErrReadOnly
	}
	var found bool
	err := o.shared.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(controlRecordsBucket)
		if bucket == nil {
			return nil
		}
		key := controlRecordKey(family, id)
		found = bucket.Get(key) != nil
		if found {
			return bucket.Delete(key)
		}
		return nil
	})
	return found, err
}

// DeleteControlRecordWithMarker makes configured-record retirement atomic with
// its durable marker, so a crash cannot re-seed a deleted record on restart.
func (o *Outbox) DeleteControlRecordWithMarker(family, id, markerFamily string, marker []byte) (bool, error) {
	if o == nil || o.shared == nil || family == "" || id == "" || markerFamily == "" {
		return false, fmt.Errorf("control record store and keys are required")
	}
	if o.shared.readOnly {
		return false, ErrReadOnly
	}
	var found bool
	err := o.shared.db.Update(func(tx *bbolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(controlRecordsBucket)
		if err != nil {
			return err
		}
		key := controlRecordKey(family, id)
		found = bucket.Get(key) != nil
		if !found {
			return nil
		}
		if err := bucket.Put(controlRecordKey(markerFamily, id), marker); err != nil {
			return err
		}
		return bucket.Delete(key)
	})
	return found, err
}
