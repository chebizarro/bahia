package mmm

import (
	"encoding/binary"

	"fiatjaf.com/nostr"
	"github.com/PowerDNS/lmdb-go/lmdb"
)

func (b *MultiMmapManager) DebugAtPosition(offset uint64) (idPrefix []byte, layers []*IndexingLayer) {
	b.lmdbEnv.View(func(txn *lmdb.Txn) error {
		txn.RawRead = true

		cursor, err := txn.OpenCursor(b.indexId)
		if err != nil {
			panic(err)
		}
		defer cursor.Close()

		for key, val, err := cursor.Get(nil, nil, lmdb.First); err == nil; key, val, err = cursor.Get(key, val, lmdb.Next) {
			pos := positionFromBytes(val[0:12])

			if pos.start == offset {
				idPrefix = key
				layers = make([]*IndexingLayer, 0, (len(val)-12)/2)

				for s := 12; s < len(val); s += 2 {
					layer := b.layers.ByID(binary.BigEndian.Uint16(val[s : s+2]))
					layers = append(layers, layer)
				}

				return nil
			}
		}

		return nil
	})

	return
}

func (il *IndexingLayer) HasAtPosition(offset uint64) (
	exists bool,
	createdAt nostr.Timestamp,
	kind nostr.Kind,
) {
	il.lmdbEnv.View(func(txn *lmdb.Txn) error {
		txn.RawRead = true

		{
			cursor, err := txn.OpenCursor(il.indexCreatedAt)
			if err != nil {
				panic(err)
			}
			defer cursor.Close()

			for key, val, err := cursor.Get(nil, nil, lmdb.First); err == nil; key, val, err = cursor.Get(key, val, lmdb.Next) {
				pos := positionFromBytes(val[0:12])

				if pos.start == offset {
					exists = true
					createdAt = nostr.Timestamp(binary.BigEndian.Uint32(key))
					break
				}
			}
		}

		{
			cursor, err := txn.OpenCursor(il.indexKind)
			if err != nil {
				panic(err)
			}
			defer cursor.Close()

			for key, val, err := cursor.Get(nil, nil, lmdb.First); err == nil; key, val, err = cursor.Get(key, val, lmdb.Next) {
				pos := positionFromBytes(val[0:12])
				if pos.start == offset {
					exists = true
					kind = nostr.Kind(binary.BigEndian.Uint16(key))
					break
				}
			}
		}

		return nil
	})

	return
}
