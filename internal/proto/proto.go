// Package proto implements the frozen tbd v1 wire protocol (proto/PROTOCOL.md, BRIEF §3):
// pairing secret and code, key and topic derivation, padding and ChaCha20-Poly1305 sealing.
// Any change here is a protocol version bump and must regenerate proto/vectors.json.
package proto

import (
	"bytes"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/chacha20poly1305"
)

// Protocol constants.
const (
	Version    = 1
	SecretSize = 16
	CodeLen    = 26
	QRPrefix   = "tbd:v1:"
	AAD        = "tbd:v1"
	KeySize    = chacha20poly1305.KeySize
	NonceSize  = chacha20poly1305.NonceSize
	TagSize    = chacha20poly1305.Overhead
	SmallPad   = 1024
	LargePad   = 2048
	// MaxPayload is the largest JSON document that fits LargePad (2 bytes go to the length prefix).
	MaxPayload = LargePad - 2
	// FCMDataLimit is FCM's limit on the data payload; the sealed "c" value is far below it.
	FCMDataLimit = 4096
	TopicLen     = 32
	infoEnc      = "tbd-enc"
	infoTopic    = "tbd-topic"
	ellipsis     = "…"
)

// Message types (field "t").
const (
	TypeMsg   = "msg"
	TypeAlert = "alert"
	TypeHB    = "hb"
	TypePair  = "pair"
)

// Alert kinds (field "kind").
const (
	KindSessionExpired = "session_expired"
	KindBrowserDead    = "browser_dead"
	KindDaemonStopping = "daemon_stopping"
)

var codeEnc = base32.StdEncoding.WithPadding(base32.NoPadding)

// Errors returned by the decoding side.
var (
	ErrCode      = errors.New("proto: invalid pairing code")
	ErrPadding   = errors.New("proto: invalid padded plaintext")
	ErrTooLarge  = errors.New("proto: message does not fit the largest padding size")
	ErrSealed    = errors.New("proto: invalid sealed payload")
	ErrAuth      = errors.New("proto: message authentication failed")
	ErrMessage   = errors.New("proto: invalid message")
	ErrRandom    = errors.New("proto: random source failed")
	errEmptyBody = errors.New("proto: nothing left to truncate")
)

// Secret is the 16-byte pairing secret S.
type Secret [SecretSize]byte

// NewSecret draws S from r (crypto/rand.Reader when r is nil).
func NewSecret(r io.Reader) (Secret, error) {
	if r == nil {
		r = rand.Reader
	}
	var s Secret
	if _, err := io.ReadFull(r, s[:]); err != nil {
		return Secret{}, fmt.Errorf("%w: %v", ErrRandom, err)
	}
	return s, nil
}

// Code is the 26-character RFC 4648 base32 form of S (uppercase, no padding).
func (s Secret) Code() string { return codeEnc.EncodeToString(s[:]) }

// DisplayCode groups the code as XXXX-XXXX-XXXX-XXXX-XXXX-XXXX-XX.
func (s Secret) DisplayCode() string {
	c := s.Code()
	var b strings.Builder
	for i := 0; i < len(c); i += 4 {
		if i > 0 {
			b.WriteByte('-')
		}
		b.WriteString(c[i:min(i+4, len(c))])
	}
	return b.String()
}

// QRText is the text encoded in the pairing QR code.
func (s Secret) QRText() string { return QRPrefix + s.Code() }

// ParseCode accepts the 26-character code in any case, with or without dashes and spaces, and
// optionally prefixed with "tbd:v1:" (the QR text). Non-canonical trailing bits are rejected.
func ParseCode(in string) (Secret, error) {
	in = strings.TrimSpace(in)
	if len(in) >= len(QRPrefix) && strings.EqualFold(in[:len(QRPrefix)], QRPrefix) {
		in = in[len(QRPrefix):]
	}
	var b strings.Builder
	for _, r := range in {
		switch {
		case r == '-' || r == ' ':
			continue
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 'a' + 'A')
		default:
			b.WriteRune(r)
		}
	}
	c := b.String()
	if len(c) != CodeLen {
		return Secret{}, fmt.Errorf("%w: want %d characters, got %d", ErrCode, CodeLen, len(c))
	}
	raw, err := codeEnc.DecodeString(c)
	// Re-encoding catches non-zero trailing bits in the last character (canonical form only).
	if err != nil || len(raw) != SecretSize || codeEnc.EncodeToString(raw) != c {
		return Secret{}, fmt.Errorf("%w: not canonical base32", ErrCode)
	}
	var s Secret
	copy(s[:], raw)
	return s, nil
}

func hkdf32(s Secret, info string, n int) []byte {
	out, err := hkdf.Key(sha256.New, s[:], nil, info, n)
	if err != nil { // only possible for absurd lengths
		panic(err)
	}
	return out
}

// Key derives K = HKDF-SHA256(ikm=S, salt="", info="tbd-enc", L=32).
func (s Secret) Key() [KeySize]byte {
	var k [KeySize]byte
	copy(k[:], hkdf32(s, infoEnc, KeySize))
	return k
}

// Topic derives base64url_nopad(HKDF-SHA256(ikm=S, salt="", info="tbd-topic", L=24)), 32 chars.
func (s Secret) Topic() string {
	return base64.RawURLEncoding.EncodeToString(hkdf32(s, infoTopic, 24))
}

// Message is the JSON document inside the sealed payload.
type Message struct {
	V     int    `json:"v"`
	T     string `json:"t"`
	ID    string `json:"id"`
	TS    int64  `json:"ts"`
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	URL   string `json:"url,omitempty"`
	Tag   string `json:"tag,omitempty"`
	Kind  string `json:"kind,omitempty"`
}

// NewID returns a random RFC 4122 version 4 UUID.
func NewID(r io.Reader) (string, error) {
	if r == nil {
		r = rand.Reader
	}
	var u [16]byte
	if _, err := io.ReadFull(r, u[:]); err != nil {
		return "", fmt.Errorf("%w: %v", ErrRandom, err)
	}
	u[6] = u[6]&0x0f | 0x40
	u[8] = u[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16]), nil
}

// Validate checks the fields every receiver relies on.
func (m Message) Validate() error {
	if m.V != Version {
		return fmt.Errorf("%w: version %d", ErrMessage, m.V)
	}
	switch m.T {
	case TypeMsg, TypeAlert, TypeHB, TypePair:
	default:
		return fmt.Errorf("%w: type %q", ErrMessage, m.T)
	}
	if m.ID == "" {
		return fmt.Errorf("%w: empty id", ErrMessage)
	}
	return nil
}

func marshal(m Message) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// truncateRunes keeps the longest prefix of s (at most n runes) and appends "…".
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ellipsis
	}
	i := 0
	for j := range s {
		if i == n {
			return s[:j] + ellipsis
		}
		i++
	}
	return s + ellipsis
}

// fitField shrinks *field (rune-wise, with "…") until marshal(m) fits MaxPayload.
func fitField(m *Message, field *string) ([]byte, error) {
	full := *field
	if full == "" {
		return nil, errEmptyBody
	}
	total := utf8.RuneCountInString(full)
	// Largest n in [0, total) such that the truncated message fits.
	lo, hi := -1, total-1
	var best []byte
	for lo < hi {
		mid := (lo + hi + 1) / 2
		*field = truncateRunes(full, mid)
		b, err := marshal(*m)
		if err != nil {
			return nil, err
		}
		if len(b) <= MaxPayload {
			lo, best = mid, b
		} else {
			hi = mid - 1
		}
	}
	if best == nil {
		*field = ""
		b, err := marshal(*m)
		if err != nil {
			return nil, err
		}
		if len(b) <= MaxPayload {
			return b, nil
		}
		return nil, errEmptyBody
	}
	*field = truncateRunes(full, lo)
	return best, nil
}

// EncodeJSON marshals m, truncating it so that it always fits LargePad: body first, then title
// (both with "…"), then tag and url are dropped. It returns the JSON and the message as sent.
func EncodeJSON(m Message) ([]byte, Message, error) {
	b, err := marshal(m)
	if err != nil {
		return nil, m, err
	}
	if len(b) <= MaxPayload {
		return b, m, nil
	}
	if b, err := fitField(&m, &m.Body); err == nil {
		return b, m, nil
	}
	if b, err := fitField(&m, &m.Title); err == nil {
		return b, m, nil
	}
	for _, f := range []*string{&m.Tag, &m.URL, &m.Kind} {
		*f = ""
		if b, err := marshal(m); err == nil && len(b) <= MaxPayload {
			return b, m, nil
		}
	}
	return nil, m, ErrTooLarge
}

// PadSize is the padded length for a JSON document of n bytes, or 0 if it does not fit.
func PadSize(n int) int {
	switch {
	case n+2 <= SmallPad:
		return SmallPad
	case n+2 <= LargePad:
		return LargePad
	}
	return 0
}

// Pad builds u16be(len(json)) || json || zeros, padded to 1024 or 2048 bytes.
func Pad(js []byte) ([]byte, error) {
	size := PadSize(len(js))
	if size == 0 {
		return nil, ErrTooLarge
	}
	out := make([]byte, size)
	binary.BigEndian.PutUint16(out, uint16(len(js)))
	copy(out[2:], js)
	return out, nil
}

// Unpad reverses Pad. The total length must be 1024 or 2048 and the padding must be zero.
func Unpad(p []byte) ([]byte, error) {
	if len(p) != SmallPad && len(p) != LargePad {
		return nil, fmt.Errorf("%w: length %d", ErrPadding, len(p))
	}
	n := int(binary.BigEndian.Uint16(p))
	if n == 0 || n > len(p)-2 {
		return nil, fmt.Errorf("%w: declared length %d", ErrPadding, n)
	}
	if len(p) == LargePad && n+2 <= SmallPad {
		return nil, fmt.Errorf("%w: %d bytes should have used the small size", ErrPadding, n)
	}
	for _, c := range p[2+n:] {
		if c != 0 {
			return nil, fmt.Errorf("%w: non-zero padding", ErrPadding)
		}
	}
	return p[2 : 2+n], nil
}

// Seal encrypts a padded plaintext with ChaCha20-Poly1305 and returns nonce || ciphertext.
func Seal(key [KeySize]byte, nonce, padded []byte) ([]byte, error) {
	if len(nonce) != NonceSize {
		return nil, fmt.Errorf("proto: nonce must be %d bytes", NonceSize)
	}
	aead, err := chacha20poly1305.New(key[:])
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, NonceSize+len(padded)+TagSize)
	out = append(out, nonce...)
	return aead.Seal(out, nonce, padded, []byte(AAD)), nil
}

// Open decrypts nonce || ciphertext and returns the padded plaintext.
func Open(key [KeySize]byte, sealed []byte) ([]byte, error) {
	if l := len(sealed) - NonceSize - TagSize; l != SmallPad && l != LargePad {
		return nil, fmt.Errorf("%w: length %d", ErrSealed, len(sealed))
	}
	aead, err := chacha20poly1305.New(key[:])
	if err != nil {
		return nil, err
	}
	pt, err := aead.Open(nil, sealed[:NonceSize], sealed[NonceSize:], []byte(AAD))
	if err != nil {
		return nil, ErrAuth
	}
	return pt, nil
}

// Sealed is the result of EncryptMessage.
type Sealed struct {
	C       string  // base64std(nonce || ciphertext): the FCM data field "c"
	Sent    Message // the message after truncation
	JSON    []byte  // its JSON encoding
	PadSize int     // 1024 or 2048
}

// EncryptMessage encodes, pads and seals m with a fresh nonce from r (crypto/rand when nil).
func EncryptMessage(key [KeySize]byte, m Message, r io.Reader) (Sealed, error) {
	if r == nil {
		r = rand.Reader
	}
	if err := m.Validate(); err != nil {
		return Sealed{}, err
	}
	js, sent, err := EncodeJSON(m)
	if err != nil {
		return Sealed{}, err
	}
	padded, err := Pad(js)
	if err != nil {
		return Sealed{}, err
	}
	nonce := make([]byte, NonceSize)
	if _, err := io.ReadFull(r, nonce); err != nil {
		return Sealed{}, fmt.Errorf("%w: %v", ErrRandom, err)
	}
	sealed, err := Seal(key, nonce, padded)
	if err != nil {
		return Sealed{}, err
	}
	return Sealed{C: base64.StdEncoding.EncodeToString(sealed), Sent: sent, JSON: js, PadSize: len(padded)}, nil
}

// DecryptMessage reverses EncryptMessage. It does not apply the receiver's age or replay rules
// (see Filter).
func DecryptMessage(key [KeySize]byte, c string) (Message, error) {
	sealed, err := base64.StdEncoding.DecodeString(c)
	if err != nil {
		return Message{}, fmt.Errorf("%w: base64: %v", ErrSealed, err)
	}
	padded, err := Open(key, sealed)
	if err != nil {
		return Message{}, err
	}
	js, err := Unpad(padded)
	if err != nil {
		return Message{}, err
	}
	var m Message
	if err := json.Unmarshal(js, &m); err != nil {
		return Message{}, fmt.Errorf("%w: %v", ErrMessage, err)
	}
	if err := m.Validate(); err != nil {
		return Message{}, err
	}
	return m, nil
}
