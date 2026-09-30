package mmm

import (
	"encoding/binary"
	"fmt"
	"log"

	"fiatjaf.com/nostr"
	"github.com/PowerDNS/lmdb-go/lmdb"
)

const target = 2

func (il *IndexingLayer) migrate() error {
	return il.lmdbEnv.Update(func(txn *lmdb.Txn) error {
		val, err := txn.Get(il.settings, []byte("version"))
		if err != nil && !lmdb.IsNotFound(err) {
			return fmt.Errorf("failed to get db version: %w", err)
		}

		var version uint16 = target
		if err == nil {
			version = binary.BigEndian.Uint16(val)
		}

		// do the migrations in increasing steps (there is no rollback)
		if version < target {
			log.Printf("[mmm/%s] migration %d: reindex everything\n", il.name, target)

			if err := txn.Drop(il.indexKind, false); err != nil {
				return err
			}
			if err := txn.Drop(il.indexPubkey, false); err != nil {
				return err
			}
			if err := txn.Drop(il.indexPubkeyKind, false); err != nil {
				return err
			}
			if err := txn.Drop(il.indexTag, false); err != nil {
				return err
			}
			if err := txn.Drop(il.indexTag32, false); err != nil {
				return err
			}
			if err := txn.Drop(il.indexTagAddr, false); err != nil {
				return err
			}
			if err := txn.Drop(il.indexPTagKind, false); err != nil {
				return err
			}

			// we can't just iterate this layer's events because we don't have this index
			// so we must iterate all events in the mmap file and check if they belong to this layer
			mmmtxn, err := il.mmmm.lmdbEnv.BeginTxn(nil, lmdb.Readonly)
			if err != nil {
				return err
			}
			defer mmmtxn.Abort()

			cursor, err := mmmtxn.OpenCursor(il.mmmm.indexId)
			if err != nil {
				return fmt.Errorf("failed to open cursor in migration %d: %w", target, err)
			}
			defer cursor.Close()

			var evt nostr.Event
			var id, val []byte

			for {
				id, val, err = cursor.Get(nil, nil, lmdb.Next)
				if lmdb.IsNotFound(err) {
					break
				}
				if err != nil {
					return fmt.Errorf("failed to get next in migration %d: %w", target, err)
				}

				// check if this event belongs to this layer
				belongs := false
				for i := 12; i < len(val); i += 2 {
					ilid := binary.BigEndian.Uint16(val[i : i+2])
					if ilid == il.id {
						belongs = true
						break
					}
				}
				if !belongs {
					continue
				}

				// load event and reindex
				pos := positionFromBytes(val[0:12])
				if err := il.mmmm.loadEvent(pos, &evt); err != nil {
					log.Printf("failed to load event %x for reindexing on layer %s: %s", id, il.name, err)
					continue
				}

				for key := range il.getIndexKeysForEvent(evt) {
					if err := txn.Put(key.dbi, key.key, val[0:12], 0); err != nil {
						return fmt.Errorf("failed to save index for event %s on migration %d: %w", evt.ID, target, err)
					}
				}
			}

			// bump version
			if err := il.setVersion(txn, target); err != nil {
				return err
			}
		}

		return nil
	})
}

func (il *IndexingLayer) setVersion(txn *lmdb.Txn, v uint16) error {
	var newVersion [2]byte
	binary.BigEndian.PutUint16(newVersion[:], v)
	return txn.Put(il.settings, []byte("version"), newVersion[:], 0)
}
