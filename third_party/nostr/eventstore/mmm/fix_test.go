package mmm

import (
	"bytes"
	"fmt"
	"iter"
	"math/rand/v2"
	"os"
	"slices"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/PowerDNS/lmdb-go/lmdb"
	"github.com/stretchr/testify/require"
)

func FuzzBorkedRescan(f *testing.F) {
	f.Add(0, uint(3), uint(150), uint(40), uint(30), uint(30))
	f.Fuzz(func(t *testing.T, seed int, nlayers, nevents, layerProbability, inconsistencyProbability, borkProbability uint) {
		nlayers = nlayers%20 + 1
		nevents = nevents%100 + 1
		layerProbability = layerProbability % 100
		borkProbability = borkProbability % 100
		inconsistencyProbability = inconsistencyProbability % 100

		// create a temporary directory for the test
		tmpDir, err := os.MkdirTemp("", "mmm-rescan-test-*")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		rnd := rand.New(rand.NewPCG(uint64(seed), 0))
		chance := func(n uint) bool {
			return rnd.UintN(100) < n
		}

		// initialize MMM
		mmmm := &MultiMmapManager{Dir: tmpDir}

		err = mmmm.Init()
		require.NoError(t, err)
		defer mmmm.Close()

		// create layers
		for i := range nlayers {
			name := string([]byte{97 + byte(i)})
			il, err := mmmm.EnsureLayer(name)
			defer il.Close()
			require.NoError(t, err, "layer %s/%d", name, i)
		}

		// create and store events
		sk := nostr.MustSecretKeyFromHex("945e01e37662430162121b804d3645a86d97df9d256917d86735d0eb219393eb")
		storedEvents := make([]nostr.Event, 0, nevents)

		for i := 0; i < int(nevents); i++ {
			evt := nostr.Event{
				CreatedAt: nostr.Timestamp(i * 1000),
				Kind:      nostr.KindTextNote,
				Tags:      nostr.Tags{},
				Content:   fmt.Sprintf("test content %d", i),
			}
			evt.Sign(sk)

			// randomly assign to some layers (or none)
			for _, layer := range mmmm.layers {
				if chance(layerProbability) {
					err := layer.SaveEvent(evt)
					storedEvents = append(storedEvents, evt)
					require.NoError(t, err)
				}
			}
		}

		// check that all events are still accessible
		for _, evt := range storedEvents {
			// this event should still be accessible
			gotEvt, layers := mmmm.GetByID(evt.ID)
			require.NotNil(t, gotEvt, "stored event should still exist")
			require.NotEmpty(t, layers, "stored event should have layer references")
		}

		// bork some events
		type entry struct {
			evt   nostr.Event
			layer *IndexingLayer
		}
		var inconsistentEvents []entry
		var borkedEvents []nostr.Event

		err = mmmm.lmdbEnv.Update(func(txn *lmdb.Txn) error {
			cursor, err := txn.OpenCursor(mmmm.indexId)
			require.NoError(t, err)
			defer cursor.Close()

			for key, val, err := cursor.Get(nil, nil, lmdb.First); err == nil; key, val, err = cursor.Get(key, val, lmdb.Next) {
				pos := positionFromBytes(val[0:12])

				if chance(borkProbability) {
					var evt nostr.Event
					mmmm.loadEvent(pos, &evt)
					borkedEvents = append(borkedEvents, evt)

					// manually corrupt the mmapped file at these positions
					copy(mmmm.mmapf[pos.start:], []byte("XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"))
				} else if chance(inconsistencyProbability) {
					// inconsistently delete from some layers
					var evt nostr.Event
					mmmm.loadEvent(pos, &evt)

					// manually delete indexes from some layer
					_, layers := mmmm.GetByID(evt.ID)

					// this won't be erased, just removed from this specific layer
					layer := layers[rnd.IntN(len(layers))]
					posb := make([]byte, 12)
					writeBytesFromPosition(posb, pos)

					if err := layer.lmdbEnv.Update(func(iltxn *lmdb.Txn) error {
						return layer.deleteIndexes(iltxn, evt, posb)
					}); err != nil {
						panic(err)
					}

					if len(layers) == 1 {
						// this should be completely erased since there is only one layer, so
						// for checking purposes in this test just treat it as borked
						borkedEvents = append(borkedEvents, evt)
					} else {
						inconsistentEvents = append(inconsistentEvents, entry{evt: evt, layer: layer})
					}
				}
			}
			return nil
		})
		require.NoError(t, err)

		// call Rescan() to remove borked and inconsistent events
		err = mmmm.Rescan()
		require.NoError(t, err)

		// verify borked events are no longer accessible
		for _, evt := range borkedEvents {
			gotEvt, layers := mmmm.GetByID(evt.ID)
			require.Nilf(t, gotEvt, "borked event %s should have been removed", evt.ID)
			require.Empty(t, layers, "borked event should have no layer references")
		}

		// check that non-borked events are still accessible
		for _, evt := range storedEvents {
			isBorked := slices.ContainsFunc(borkedEvents, func(b nostr.Event) bool {
				return bytes.Equal(evt.ID[:], b.ID[:])
			})
			if !isBorked {
				// this event should still be accessible
				gotEvt, layers := mmmm.GetByID(evt.ID)
				require.NotNilf(t, gotEvt, "non-borked event %s should still exist", evt.ID)
				require.NotEmpty(t, layers, "non-borked event should have layer references")
			}
		}

		// check that inconsistent events have been removed from one of their original layers
		for _, e := range inconsistentEvents {
			evt := e.evt
			layer := e.layer

			_, layers := mmmm.GetByID(evt.ID)
			require.NotContainsf(t, layers, layer, "layers for inconsistent event should not contain %s", layer.name)

			next, done := iter.Pull(layer.QueryEvents(nostr.Filter{
				Since: evt.CreatedAt - 1,
				Until: evt.CreatedAt + 1,
			}, 1))
			evt, ok := next()
			done()

			require.False(t, ok, "layer for inconsistent event should not index %s", evt.ID)
		}
	})
}
