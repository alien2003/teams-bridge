package proto

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestVectorsFileIsUpToDate(t *testing.T) {
	want := MarshalVectors(GenerateVectors())
	got, err := os.ReadFile("../../proto/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("proto/vectors.json is stale: run `mise run gen-vectors` (and bump the protocol version if this was not intended)")
	}
}

func TestVectorsDecrypt(t *testing.T) {
	var v Vectors
	raw, err := os.ReadFile("../../proto/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	sb, _ := hex.DecodeString(v.SecretHex)
	var s Secret
	copy(s[:], sb)
	if s.Code() != v.Code || s.Topic() != v.Topic || hex.EncodeToString(func() []byte { k := s.Key(); return k[:] }()) != v.KeyHex {
		t.Fatal("derived values do not match the vectors")
	}
	k := s.Key()
	for _, mv := range v.Messages {
		m, err := DecryptMessage(k, mv.SealedB64)
		if err != nil {
			t.Fatalf("%s: %v", mv.Name, err)
		}
		js, _, err := EncodeJSON(m)
		if err != nil || string(js) != mv.JSON {
			t.Fatalf("%s: re-encoded JSON differs", mv.Name)
		}
		var req struct {
			Message struct {
				Topic   string               `json:"topic"`
				Data    map[string]string    `json:"data"`
				Android struct{ TTL string } `json:"android"`
			} `json:"message"`
		}
		if err := json.Unmarshal(mv.FCMRequest, &req); err != nil || req.Message.Topic != v.Topic || req.Message.Data["c"] != mv.SealedB64 {
			t.Fatalf("%s: bad fcm_request", mv.Name)
		}
		if wantTTL := map[bool]string{true: "21600s", false: "3600s"}[mv.Type == TypeHB]; req.Message.Android.TTL != wantTTL {
			t.Fatalf("%s: ttl %s, want %s", mv.Name, req.Message.Android.TTL, wantTTL)
		}
	}
	for _, iv := range v.Invalid {
		if _, err := DecryptMessage(k, iv.SealedB64); err == nil {
			t.Fatalf("invalid vector %s decrypted", iv.Name)
		}
	}
	for _, cv := range v.CodeInputs {
		got, err := ParseCode(cv.Input)
		if cv.Valid != (err == nil) {
			t.Fatalf("code %q (%s): err=%v, want valid=%v", cv.Input, cv.Note, err, cv.Valid)
		}
		if cv.Valid && hex.EncodeToString(got[:]) != cv.SecretHex {
			t.Fatalf("code %q decoded wrong", cv.Input)
		}
	}
}

var topicRE = regexp.MustCompile(`^[a-zA-Z0-9-_.~%]{1,900}$`)

func TestSecretRoundTrip(t *testing.T) {
	for range 200 {
		s, err := NewSecret(nil)
		if err != nil {
			t.Fatal(err)
		}
		c := s.Code()
		if len(c) != CodeLen || strings.ToUpper(c) != c {
			t.Fatalf("bad code %q", c)
		}
		d := s.DisplayCode()
		if len(d) != CodeLen+6 || strings.Count(d, "-") != 6 {
			t.Fatalf("bad display code %q", d)
		}
		for _, in := range []string{c, d, strings.ToLower(d), s.QRText(), "TBD:V1:" + c} {
			got, err := ParseCode(in)
			if err != nil || got != s {
				t.Fatalf("ParseCode(%q) = %v, %v", in, got, err)
			}
		}
		if tp := s.Topic(); len(tp) != TopicLen || !topicRE.MatchString(tp) {
			t.Fatalf("bad topic %q", tp)
		}
	}
}

func TestNewSecretRandomFailure(t *testing.T) {
	if _, err := NewSecret(bytes.NewReader([]byte{1, 2, 3})); !errors.Is(err, ErrRandom) {
		t.Fatalf("err = %v", err)
	}
}

func TestPadBoundaries(t *testing.T) {
	for _, tc := range []struct{ n, want int }{{1, 1024}, {1022, 1024}, {1023, 2048}, {2046, 2048}, {2047, 0}} {
		if got := PadSize(tc.n); got != tc.want {
			t.Errorf("PadSize(%d) = %d, want %d", tc.n, got, tc.want)
		}
		js := bytes.Repeat([]byte("x"), tc.n)
		p, err := Pad(js)
		if tc.want == 0 {
			if !errors.Is(err, ErrTooLarge) {
				t.Errorf("Pad(%d): err = %v", tc.n, err)
			}
			continue
		}
		if err != nil || len(p) != tc.want {
			t.Fatalf("Pad(%d) = %d bytes, %v", tc.n, len(p), err)
		}
		back, err := Unpad(p)
		if err != nil || !bytes.Equal(back, js) {
			t.Fatalf("Unpad(Pad(%d)): %v", tc.n, err)
		}
	}
	if _, err := Unpad(make([]byte, 1024)); !errors.Is(err, ErrPadding) {
		t.Fatal("zero length must be rejected")
	}
}

func TestTruncationAlwaysFits(t *testing.T) {
	k := VectorSecret.Key()
	gen := func(n int, alphabet []rune) string {
		var b strings.Builder
		for range n {
			i, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
			b.WriteRune(alphabet[i.Int64()])
		}
		return b.String()
	}
	alphabets := [][]rune{[]rune("abc"), []rune("ж😀\"\\\n\x01<>&"), []rune(" \x00\x1f")}
	for i := range 300 {
		a := alphabets[i%len(alphabets)]
		m := Message{V: 1, T: TypeMsg, ID: "id", TS: 1, Title: gen(i%7*200, a), Body: gen(i*13, a), URL: gen(i%5*300, a), Tag: gen(i%3*400, a)}
		s, err := EncryptMessage(k, m, nil)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if len(s.JSON) > MaxPayload || s.PadSize != PadSize(len(s.JSON)) {
			t.Fatalf("case %d: %d bytes in %d", i, len(s.JSON), s.PadSize)
		}
		if !utf8.ValidString(s.Sent.Body) || !utf8.ValidString(s.Sent.Title) {
			t.Fatalf("case %d: truncation broke UTF-8", i)
		}
		if s.Sent.Body != m.Body && !strings.HasSuffix(s.Sent.Body, "…") && s.Sent.Body != "" {
			t.Fatalf("case %d: truncated body without ellipsis", i)
		}
		// FCM limit on the whole data payload ({"c": "..."}).
		data, _ := json.Marshal(map[string]string{"c": s.C})
		if len(data) > FCMDataLimit {
			t.Fatalf("case %d: data payload %d > %d", i, len(data), FCMDataLimit)
		}
		got, err := DecryptMessage(k, s.C)
		if err != nil || got != s.Sent {
			t.Fatalf("case %d: round trip: %v", i, err)
		}
	}
}

func TestBodyTruncatedFirstAndMaximally(t *testing.T) {
	k := VectorSecret.Key()
	m := Message{V: 1, T: TypeMsg, ID: "x", TS: 1, Title: "Alice", Body: strings.Repeat("é", 5000), URL: "https://teams.microsoft.com/v2/", Tag: "19:abc@thread.v2"}
	s, err := EncryptMessage(k, m, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Sent.Title != m.Title || s.Sent.URL != m.URL || s.Sent.Tag != m.Tag {
		t.Fatal("only the body should have been truncated")
	}
	if s.PadSize != LargePad || len(s.JSON) > MaxPayload || len(s.JSON) < MaxPayload-3 {
		t.Fatalf("expected a nearly full 2048 block, got %d bytes", len(s.JSON))
	}
}

func TestSizesAreFixed(t *testing.T) {
	k := VectorSecret.Key()
	sizes := map[int]bool{}
	for _, n := range []int{0, 10, 500, 900, 1000, 1500, 3000} {
		s, err := EncryptMessage(k, Message{V: 1, T: TypeMsg, ID: "x", TS: 1, Body: strings.Repeat("a", n)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := base64.StdEncoding.DecodeString(s.C)
		sizes[len(raw)] = true
	}
	if len(sizes) != 2 || !sizes[NonceSize+SmallPad+TagSize] || !sizes[NonceSize+LargePad+TagSize] {
		t.Fatalf("ciphertext sizes %v", sizes)
	}
}

func TestDecryptRejects(t *testing.T) {
	k := VectorSecret.Key()
	s, err := EncryptMessage(k, Message{V: 1, T: TypeHB, ID: "x", TS: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	other := Secret{1}.Key()
	if _, err := DecryptMessage(other, s.C); !errors.Is(err, ErrAuth) {
		t.Fatalf("wrong key: %v", err)
	}
	if _, err := DecryptMessage(k, "!!!"); !errors.Is(err, ErrSealed) {
		t.Fatalf("bad base64: %v", err)
	}
	if _, err := DecryptMessage(k, base64.StdEncoding.EncodeToString(make([]byte, 100))); !errors.Is(err, ErrSealed) {
		t.Fatalf("short: %v", err)
	}
	if _, err := EncryptMessage(k, Message{V: 2, T: TypeHB, ID: "x"}, nil); !errors.Is(err, ErrMessage) {
		t.Fatal("v=2 must not be sent")
	}
}

func TestNewID(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for range 100 {
		id, err := NewID(nil)
		if err != nil || !re.MatchString(id) || seen[id] {
			t.Fatalf("bad id %q %v", id, err)
		}
		seen[id] = true
	}
}

func TestFilter(t *testing.T) {
	now := time.Unix(1767225600, 0)
	var f Filter
	m := Message{V: 1, T: TypeMsg, ID: "a", TS: now.Unix()}
	if !f.Accept(m, now) || f.Accept(m, now) {
		t.Fatal("duplicate must be dropped")
	}
	old := Message{V: 1, T: TypeMsg, ID: "old", TS: now.Add(-MaxAge - time.Second).Unix()}
	if f.Accept(old, now) {
		t.Fatal("older than 24h must be dropped")
	}
	edge := Message{V: 1, T: TypeMsg, ID: "edge", TS: now.Add(-MaxAge).Unix()}
	if !f.Accept(edge, now) {
		t.Fatal("exactly 24h old is still accepted")
	}
	for i := range ReplaySize {
		f.Accept(Message{V: 1, T: TypeMsg, ID: strings.Repeat("x", i+1), TS: now.Unix()}, now)
	}
	if !f.Accept(m, now) {
		t.Fatal("id evicted after 256 newer ids must be accepted again")
	}
}

func TestFilterExtremeTimestamps(t *testing.T) {
	now := time.Unix(1767225600, 0)
	var f Filter
	for i, tc := range []struct {
		ts   int64
		want bool
	}{{math.MaxInt64, true}, {math.MinInt64, false}, {0, false}, {-1, false}, {now.Unix(), true}} {
		if got := f.Accept(Message{V: 1, T: TypeMsg, ID: strconv.Itoa(i), TS: tc.ts}, now); got != tc.want {
			t.Errorf("ts %d: accepted=%v, want %v", tc.ts, got, tc.want)
		}
	}
}
