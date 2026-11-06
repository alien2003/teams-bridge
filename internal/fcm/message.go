package fcm

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/alien2003/teams-bridge/internal/proto"
)

// SendMessage seals m for the pairing secret and sends it to the secret's topic. It returns the
// FCM message name and the message as it was finally sent (body or title may be truncated).
//
// FCM's error reference documents a 2048-byte data limit for topic messages (4096 otherwise).
// A message padded to 2048 bytes has a 2768-character payload; if FCM rejects such a payload
// with 400 INVALID_ARGUMENT, SendMessage re-seals it once, truncated to fit the 1024-byte
// padding (1404 characters), with the same id. Receivers accept both sizes (proto/PROTOCOL.md
// §4), so the protocol is unchanged.
func (c *Client) SendMessage(ctx context.Context, secret proto.Secret, m proto.Message, rnd io.Reader) (string, proto.Message, error) {
	key, topic := secret.Key(), secret.Topic()
	sealed, err := proto.EncryptMessage(key, m, rnd)
	if err != nil {
		return "", m, err
	}
	name, err := c.Send(ctx, topic, sealed.C, m.T)
	if err == nil || sealed.PadSize != proto.LargePad {
		return name, sealed.Sent, err
	}
	var fe *Error
	if !errors.As(err, &fe) || fe.HTTPStatus != http.StatusBadRequest {
		return "", sealed.Sent, err
	}
	small, ok := FitSmall(m)
	if !ok {
		return "", sealed.Sent, err
	}
	c.log.Warn("fcm: large payload rejected as invalid; resending it truncated to the small size",
		"type", m.T, "status", fe.Status)
	resealed, err2 := proto.EncryptMessage(key, small, rnd)
	if err2 != nil {
		return "", sealed.Sent, err
	}
	name, err2 = c.Send(ctx, topic, resealed.C, m.T)
	return name, resealed.Sent, err2
}

// FitSmall truncates m (body first, then title, rune-wise with "…", like proto.EncodeJSON) so that
// its JSON fits the 1024-byte padding. ok is false if even empty fields do not fit.
func FitSmall(m proto.Message) (proto.Message, bool) {
	fits := func(x proto.Message) bool {
		js, _, err := proto.EncodeJSON(x)
		return err == nil && proto.PadSize(len(js)) == proto.SmallPad
	}
	if fits(m) {
		return m, true
	}
	for _, field := range []func(*proto.Message) *string{
		func(x *proto.Message) *string { return &x.Body },
		func(x *proto.Message) *string { return &x.Title },
	} {
		runes := []rune(*field(&m))
		if len(runes) == 0 {
			continue
		}
		with := func(n int) proto.Message {
			x := m
			if n < 0 {
				*field(&x) = ""
			} else {
				*field(&x) = string(runes[:n]) + "…"
			}
			return x
		}
		// Largest n in [0, len) whose truncation fits; the JSON length is monotonic in n.
		lo, hi := -1, len(runes)-1
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if fits(with(mid)) {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		if lo >= 0 {
			return with(lo), true
		}
		m = with(-1)
		if fits(m) {
			return m, true
		}
	}
	return m, false
}
