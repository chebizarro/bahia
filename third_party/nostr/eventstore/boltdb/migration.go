package boltdb

import (
	"encoding/binary"
	"log"

	"go.etcd.io/bbolt"
)

const (
	DB_VERSION byte = 'v'
)

const target = 1

func (b *BoltBackend) migrate() error {
	return b.DB.Update(func(txn *bbolt.Tx) error {
		bucket := txn.Bucket(settingsStore)

		val := bucket.Get([]byte("version"))

		var version uint16 = target
		if val != nil {
			version = binary.BigEndian.Uint16(val)
		}

		// do the migrations in increasing steps (there is no rollback)
		if version < target {
			log.Printf("[bolt] migration %d: reindex everything\n", target)

			// bump version
			if err := b.setVersion(txn, target); err != nil {
				return err
			}
		}

		return nil
	})
}

func (b *BoltBackend) setVersion(txn *bbolt.Tx, v uint16) error {
	bucket := txn.Bucket(settingsStore)

	var newVersion [2]byte
	binary.BigEndian.PutUint16(newVersion[:], v)
	return bucket.Put([]byte("version"), newVersion[:])
}
