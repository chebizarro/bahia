package domain

import (
	"testing"
	"time"
)

func TestRevisionTimesUsePostgresPrecision(t *testing.T) {
	nanos := time.Date(2026, 10, 1, 12, 0, 0, 123456789, time.FixedZone("x", 3600))
	got := NormalizeRevisionTime(nanos)
	if want := time.Date(2026, 10, 1, 11, 0, 0, 123456000, time.UTC); !got.Equal(want) || got.Location() != time.UTC {
		t.Fatalf("NormalizeRevisionTime = %s, want %s in UTC", got, want)
	}
	if !NormalizeRevisionTime(time.Time{}).IsZero() {
		t.Fatal("zero revision must stay zero")
	}
	if next := NextRevisionTime(nanos); next.Nanosecond()%1000 != 0 || !next.After(nanos) {
		t.Fatalf("NextRevisionTime(%s) = %s, want a microsecond revision after it", nanos, next)
	}
	future := time.Now().Add(time.Hour)
	if next := NextRevisionTime(future); !next.Equal(NormalizeRevisionTime(future).Add(time.Microsecond)) {
		t.Fatalf("NextRevisionTime of a future revision = %s, want it plus one microsecond", next)
	}
	if !SameRevision(nanos, NormalizeRevisionTime(nanos)) || SameRevision(nanos, nanos.Add(time.Microsecond)) {
		t.Fatal("SameRevision must ignore only sub-microsecond digits")
	}
}
