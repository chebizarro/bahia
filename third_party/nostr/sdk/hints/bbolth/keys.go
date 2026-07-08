package bbolth

import (
	"encoding/binary"

	"fiatjaf.com/nostr"
)

func encodeKey(pubkey nostr.PubKey, relay string) []byte {
	k := make([]byte, 32+len(relay))
	copy(k[0:32], pubkey[:])
	copy(k[32:], relay)
	return k
}

func parseKey(k []byte) (pubkey nostr.PubKey, relay string) {
	pubkey = [32]byte(k[0:32])
	relay = string(k[32:])
	return
}

func encodeValue(tss timestamps) []byte {
	v := make([]byte, 16)
	binary.LittleEndian.PutUint32(v[0:], uint32(tss[0]))
	binary.LittleEndian.PutUint32(v[4:], uint32(tss[1]))
	binary.LittleEndian.PutUint32(v[8:], uint32(tss[2]))
	binary.LittleEndian.PutUint32(v[12:], uint32(tss[3]))
	return v
}

func parseValue(v []byte) timestamps {
	return timestamps{
		nostr.Timestamp(binary.LittleEndian.Uint32(v[0:])),
		nostr.Timestamp(binary.LittleEndian.Uint32(v[4:])),
		nostr.Timestamp(binary.LittleEndian.Uint32(v[8:])),
		nostr.Timestamp(binary.LittleEndian.Uint32(v[12:])),
	}
}
