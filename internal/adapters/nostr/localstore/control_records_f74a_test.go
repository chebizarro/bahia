package localstore

import (
	"encoding/binary"
	"sync"
	"testing"
)

func TestControlRecordAtomicUpdateSerializesF74aDirtyGeneration(t *testing.T) {
	outbox, _ := openTempOutbox(t)
	const writers = 32
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := outbox.UpdateControlRecord("bootstrap", "f74a-canonical-v2", func(old []byte) ([]byte, error) {
				var n uint64
				if len(old) > 0 {
					n = binary.BigEndian.Uint64(old)
				}
				next := make([]byte, 8)
				binary.BigEndian.PutUint64(next, n+1)
				return next, nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, err := outbox.GetControlRecord("bootstrap", "f74a-canonical-v2")
	if err != nil {
		t.Fatal(err)
	}
	if n := binary.BigEndian.Uint64(got); n != writers {
		t.Fatalf("dirty generation %d, want %d", n, writers)
	}
}
