//go:build nip55llive

package nip55l

// memSecretService is an in-memory org.freedesktop.Secret.Service for the
// provisioned live test: enough of the Secret Service API for libsecret's
// password and search calls (one collection that is always unlocked), so a
// libsecret-backed nostr-signer-daemon on a private bus keeps its throwaway
// test key in this process and never in a real keyring.
//
// It negotiates dh-ietf1024-sha256-aes128-cbc-pkcs7 sessions as
// gnome-keyring does. libsecret 0.21.8 crashes if that is refused on a sync
// search with LOAD_SECRETS: _secret_session_open_sync reads *error, and
// secret_service_search_sync passes error == NULL.

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"maps"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	ssBusName       = "org.freedesktop.secrets"
	ssRoot          = dbus.ObjectPath("/org/freedesktop/secrets")
	ssCollection    = dbus.ObjectPath("/org/freedesktop/secrets/collection/test")
	ssAliasDefault  = dbus.ObjectPath("/org/freedesktop/secrets/aliases/default")
	ssItemPrefix    = "/org/freedesktop/secrets/collection/test/"
	ssSessionPrefix = "/org/freedesktop/secrets/session/"

	ssIfaceService    = "org.freedesktop.Secret.Service"
	ssIfaceCollection = "org.freedesktop.Secret.Collection"
	ssIfaceItem       = "org.freedesktop.Secret.Item"
	ssIfaceSession    = "org.freedesktop.Secret.Session"
	ssIfaceProps      = "org.freedesktop.DBus.Properties"

	ssPropLabel = "org.freedesktop.Secret.Item.Label"
	ssPropAttrs = "org.freedesktop.Secret.Item.Attributes"

	ssAlgPlain = "plain"
	ssAlgDH    = "dh-ietf1024-sha256-aes128-cbc-pkcs7"
)

// ssModp1024 is the RFC 2409 Oakley group 2 prime (generator 2), which the
// Secret Service DH algorithm uses.
var ssModp1024, _ = new(big.Int).SetString(
	"FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD129024E088A67CC74"+
		"020BBEA63B139B22514A08798E3404DDEF9519B3CD3A431B302B0A6DF25F1437"+
		"4FE1356D6D51C245E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7ED"+
		"EE386BFB5A899FA5AE9F24117C4B1FE649286651ECE65381FFFFFFFFFFFFFFFF", 16)

// ssSecret is the Secret Service Secret struct (oayays).
type ssSecret struct {
	Session     dbus.ObjectPath
	Params      []byte
	Value       []byte
	ContentType string
}

type ssItem struct {
	label       string
	attrs       map[string]string
	value       []byte
	contentType string
	created     uint64
	modified    uint64
}

type memSecretService struct {
	mu       sync.Mutex
	items    map[dbus.ObjectPath]*ssItem
	sessions map[dbus.ObjectPath][]byte // AES key; nil for plain
	next     int
}

// startMemSecretService claims org.freedesktop.secrets on conn and serves
// the in-memory store until conn closes.
func startMemSecretService(conn *dbus.Conn) (*memSecretService, error) {
	s := &memSecretService{items: map[dbus.ObjectPath]*ssItem{}, sessions: map[dbus.ObjectPath][]byte{}}
	tables := map[string]map[string]any{
		ssIfaceService: {
			"OpenSession": s.openSession,
			"SearchItems": s.searchItems,
			"Unlock":      s.unlock,
			"Lock":        s.unlock,
			"GetSecrets":  s.getSecrets,
			"ReadAlias":   s.readAlias,
			"SetAlias":    func(dbus.Message, string, dbus.ObjectPath) *dbus.Error { return nil },
			"CreateCollection": func(dbus.Message, map[string]dbus.Variant, string) (dbus.ObjectPath, dbus.ObjectPath, *dbus.Error) {
				return ssCollection, "/", nil
			},
		},
		ssIfaceCollection: {
			"CreateItem":  s.createItem,
			"SearchItems": s.collectionSearchItems,
			"Delete":      func(dbus.Message) (dbus.ObjectPath, *dbus.Error) { return "", ssNotSupported("collection Delete") },
		},
		ssIfaceItem: {
			"GetSecret": s.itemGetSecret,
			"SetSecret": s.itemSetSecret,
			"Delete":    s.itemDelete,
		},
		ssIfaceSession: {
			"Close": s.sessionClose,
		},
		ssIfaceProps: {
			"Get":    s.propGet,
			"GetAll": s.propGetAll,
			"Set":    s.propSet,
		},
	}
	for iface, table := range tables {
		if err := conn.ExportSubtreeMethodTable(table, ssRoot, iface); err != nil {
			return nil, fmt.Errorf("export %s: %w", iface, err)
		}
	}
	reply, err := conn.RequestName(ssBusName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", ssBusName, err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return nil, fmt.Errorf("%s is already owned on this bus", ssBusName)
	}
	return s, nil
}

func ssPath(msg dbus.Message) dbus.ObjectPath {
	p, _ := msg.Headers[dbus.FieldPath].Value().(dbus.ObjectPath)
	return p
}

func ssNoSuchObject(p dbus.ObjectPath) *dbus.Error {
	return &dbus.Error{Name: "org.freedesktop.Secret.Error.NoSuchObject", Body: []any{"no such object " + string(p)}}
}

func ssNotSupported(what string) *dbus.Error {
	return &dbus.Error{Name: "org.freedesktop.DBus.Error.NotSupported", Body: []any{what + " is not supported"}}
}

func isCollectionPath(p dbus.ObjectPath) bool { return p == ssCollection || p == ssAliasDefault }

func (s *memSecretService) openSession(_ dbus.Message, algorithm string, input dbus.Variant) (dbus.Variant, dbus.ObjectPath, *dbus.Error) {
	var key []byte
	output := dbus.MakeVariant("")
	switch algorithm {
	case ssAlgPlain:
	case ssAlgDH:
		peerBytes, ok := input.Value().([]byte)
		if !ok {
			return output, "/", &dbus.Error{Name: "org.freedesktop.DBus.Error.InvalidArgs", Body: []any{"DH input is ay"}}
		}
		var err error
		var pub []byte
		if pub, key, err = ssDHKey(peerBytes); err != nil {
			return output, "/", &dbus.Error{Name: "org.freedesktop.DBus.Error.InvalidArgs", Body: []any{err.Error()}}
		}
		output = dbus.MakeVariant(pub)
	default:
		return output, "/", ssNotSupported("algorithm " + algorithm)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	p := dbus.ObjectPath(fmt.Sprintf("%s%d", ssSessionPrefix, s.next))
	s.sessions[p] = key
	return output, p, nil
}

// ssDHKey answers a client DH public value: the server public value and the
// 16-byte AES key, HKDF-SHA256 (no salt, no info) of the shared secret
// left-padded to the prime's length, as libsecret derives it.
func ssDHKey(peerBytes []byte) (pub, key []byte, err error) {
	peer := new(big.Int).SetBytes(peerBytes)
	if peer.Cmp(big.NewInt(1)) <= 0 || peer.Cmp(new(big.Int).Sub(ssModp1024, big.NewInt(1))) >= 0 {
		return nil, nil, fmt.Errorf("DH public value out of range")
	}
	priv, err := rand.Int(rand.Reader, ssModp1024)
	if err != nil {
		return nil, nil, err
	}
	pub = new(big.Int).Exp(big.NewInt(2), priv, ssModp1024).Bytes()
	shared := new(big.Int).Exp(peer, priv, ssModp1024).FillBytes(make([]byte, (ssModp1024.BitLen()+7)/8))
	prk := hmac.New(sha256.New, make([]byte, sha256.Size))
	prk.Write(shared)
	okm := hmac.New(sha256.New, prk.Sum(nil))
	okm.Write([]byte{1})
	return pub, okm.Sum(nil)[:16], nil
}

// open returns the plaintext of a secret sent over session.
func (s *memSecretService) open(secret ssSecret) ([]byte, *dbus.Error) {
	key, ok := s.sessions[secret.Session]
	if !ok {
		return nil, &dbus.Error{Name: "org.freedesktop.Secret.Error.NoSession", Body: []any{"no such session"}}
	}
	if key == nil {
		return append([]byte(nil), secret.Value...), nil
	}
	bad := &dbus.Error{Name: "org.freedesktop.DBus.Error.InvalidArgs", Body: []any{"bad encrypted secret"}}
	if len(secret.Params) != aes.BlockSize || len(secret.Value) == 0 || len(secret.Value)%aes.BlockSize != 0 {
		return nil, bad
	}
	block, _ := aes.NewCipher(key)
	out := make([]byte, len(secret.Value))
	cipher.NewCBCDecrypter(block, secret.Params).CryptBlocks(out, secret.Value)
	n := int(out[len(out)-1])
	if n == 0 || n > aes.BlockSize || !bytes.Equal(out[len(out)-n:], bytes.Repeat([]byte{byte(n)}, n)) {
		return nil, bad
	}
	return out[:len(out)-n], nil
}

// seal returns value as a secret for session.
func (s *memSecretService) seal(value []byte, contentType string, session dbus.ObjectPath) (ssSecret, *dbus.Error) {
	key, ok := s.sessions[session]
	if !ok {
		return ssSecret{}, &dbus.Error{Name: "org.freedesktop.Secret.Error.NoSession", Body: []any{"no such session"}}
	}
	if key == nil {
		return ssSecret{Session: session, Params: []byte{}, Value: append([]byte(nil), value...), ContentType: contentType}, nil
	}
	iv := make([]byte, aes.BlockSize)
	_, _ = rand.Read(iv)
	n := aes.BlockSize - len(value)%aes.BlockSize
	padded := append(append([]byte(nil), value...), bytes.Repeat([]byte{byte(n)}, n)...)
	block, _ := aes.NewCipher(key)
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(padded, padded)
	return ssSecret{Session: session, Params: iv, Value: padded, ContentType: contentType}, nil
}

func (s *memSecretService) sessionClose(msg dbus.Message) *dbus.Error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, ssPath(msg))
	return nil
}

func (s *memSecretService) matchLocked(attrs map[string]string) []dbus.ObjectPath {
	out := []dbus.ObjectPath{}
	for p, it := range s.items {
		ok := true
		for k, v := range attrs {
			if it.attrs[k] != v {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, p)
		}
	}
	return out
}

func (s *memSecretService) searchItems(_ dbus.Message, attrs map[string]string) ([]dbus.ObjectPath, []dbus.ObjectPath, *dbus.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.matchLocked(attrs), []dbus.ObjectPath{}, nil
}

func (s *memSecretService) collectionSearchItems(msg dbus.Message, attrs map[string]string) ([]dbus.ObjectPath, *dbus.Error) {
	if !isCollectionPath(ssPath(msg)) {
		return nil, ssNoSuchObject(ssPath(msg))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.matchLocked(attrs), nil
}

func (s *memSecretService) unlock(_ dbus.Message, objects []dbus.ObjectPath) ([]dbus.ObjectPath, dbus.ObjectPath, *dbus.Error) {
	return objects, "/", nil
}

func (s *memSecretService) readAlias(_ dbus.Message, name string) (dbus.ObjectPath, *dbus.Error) {
	if name == "default" {
		return ssCollection, nil
	}
	return "/", nil
}

func (s *memSecretService) getSecrets(_ dbus.Message, items []dbus.ObjectPath, session dbus.ObjectPath) (map[dbus.ObjectPath]ssSecret, *dbus.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[dbus.ObjectPath]ssSecret{}
	for _, p := range items {
		if it, ok := s.items[p]; ok {
			sec, derr := s.seal(it.value, it.contentType, session)
			if derr != nil {
				return nil, derr
			}
			out[p] = sec
		}
	}
	return out, nil
}

func (s *memSecretService) createItem(msg dbus.Message, props map[string]dbus.Variant, secret ssSecret, replace bool) (dbus.ObjectPath, dbus.ObjectPath, *dbus.Error) {
	if !isCollectionPath(ssPath(msg)) {
		return "/", "/", ssNoSuchObject(ssPath(msg))
	}
	label, _ := props[ssPropLabel].Value().(string)
	attrs, _ := props[ssPropAttrs].Value().(map[string]string)
	if attrs == nil {
		attrs = map[string]string{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, derr := s.open(secret)
	if derr != nil {
		return "/", "/", derr
	}
	now := uint64(time.Now().Unix())
	if replace {
		for p, it := range s.items {
			if maps.Equal(it.attrs, attrs) {
				it.label, it.value, it.contentType, it.modified = label, value, secret.ContentType, now
				return p, "/", nil
			}
		}
	}
	s.next++
	p := dbus.ObjectPath(fmt.Sprintf("%si%d", ssItemPrefix, s.next))
	s.items[p] = &ssItem{label: label, attrs: maps.Clone(attrs), value: value,
		contentType: secret.ContentType, created: now, modified: now}
	return p, "/", nil
}

func (s *memSecretService) item(msg dbus.Message) (*ssItem, *dbus.Error) {
	it, ok := s.items[ssPath(msg)]
	if !ok {
		return nil, ssNoSuchObject(ssPath(msg))
	}
	return it, nil
}

func (s *memSecretService) itemGetSecret(msg dbus.Message, session dbus.ObjectPath) (ssSecret, *dbus.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, derr := s.item(msg)
	if derr != nil {
		return ssSecret{}, derr
	}
	return s.seal(it.value, it.contentType, session)
}

func (s *memSecretService) itemSetSecret(msg dbus.Message, secret ssSecret) *dbus.Error {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, derr := s.item(msg)
	if derr != nil {
		return derr
	}
	value, derr := s.open(secret)
	if derr != nil {
		return derr
	}
	it.value, it.contentType, it.modified = value, secret.ContentType, uint64(time.Now().Unix())
	return nil
}

func (s *memSecretService) itemDelete(msg dbus.Message) (dbus.ObjectPath, *dbus.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, derr := s.item(msg); derr != nil {
		return "/", derr
	}
	delete(s.items, ssPath(msg))
	return "/", nil
}

func (s *memSecretService) props(p dbus.ObjectPath, iface string) (map[string]dbus.Variant, *dbus.Error) {
	switch {
	case p == ssRoot && iface == ssIfaceService:
		return map[string]dbus.Variant{"Collections": dbus.MakeVariant([]dbus.ObjectPath{ssCollection})}, nil
	case isCollectionPath(p) && iface == ssIfaceCollection:
		items := []dbus.ObjectPath{}
		for ip := range s.items {
			items = append(items, ip)
		}
		return map[string]dbus.Variant{
			"Items":    dbus.MakeVariant(items),
			"Label":    dbus.MakeVariant("nip55l live test"),
			"Locked":   dbus.MakeVariant(false),
			"Created":  dbus.MakeVariant(uint64(0)),
			"Modified": dbus.MakeVariant(uint64(0)),
		}, nil
	case strings.HasPrefix(string(p), ssItemPrefix) && iface == ssIfaceItem:
		it, ok := s.items[p]
		if !ok {
			return nil, ssNoSuchObject(p)
		}
		return map[string]dbus.Variant{
			"Locked":     dbus.MakeVariant(false),
			"Attributes": dbus.MakeVariant(maps.Clone(it.attrs)),
			"Label":      dbus.MakeVariant(it.label),
			"Created":    dbus.MakeVariant(it.created),
			"Modified":   dbus.MakeVariant(it.modified),
		}, nil
	}
	return map[string]dbus.Variant{}, nil
}

func (s *memSecretService) propGetAll(msg dbus.Message, iface string) (map[string]dbus.Variant, *dbus.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.props(ssPath(msg), iface)
}

func (s *memSecretService) propGet(msg dbus.Message, iface, name string) (dbus.Variant, *dbus.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, derr := s.props(ssPath(msg), iface)
	if derr != nil {
		return dbus.Variant{}, derr
	}
	v, ok := all[name]
	if !ok {
		return dbus.Variant{}, &dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownProperty", Body: []any{name}}
	}
	return v, nil
}

func (s *memSecretService) propSet(msg dbus.Message, iface, name string, v dbus.Variant) *dbus.Error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if iface != ssIfaceItem {
		return ssNotSupported("setting " + iface + "." + name)
	}
	it, derr := s.item(msg)
	if derr != nil {
		return derr
	}
	switch name {
	case "Label":
		label, ok := v.Value().(string)
		if !ok {
			return &dbus.Error{Name: "org.freedesktop.DBus.Error.InvalidArgs", Body: []any{"Label is a string"}}
		}
		it.label = label
	case "Attributes":
		attrs, ok := v.Value().(map[string]string)
		if !ok {
			return &dbus.Error{Name: "org.freedesktop.DBus.Error.InvalidArgs", Body: []any{"Attributes is a{ss}"}}
		}
		it.attrs = maps.Clone(attrs)
	default:
		return ssNotSupported("setting Item." + name)
	}
	it.modified = uint64(time.Now().Unix())
	return nil
}

// count reports how many items the store holds.
func (s *memSecretService) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}
