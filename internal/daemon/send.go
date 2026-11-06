package daemon

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/alien2003/teams-bridge/internal/config"
	"github.com/alien2003/teams-bridge/internal/fcm"
	"github.com/alien2003/teams-bridge/internal/proto"
)

// Sender seals and delivers a message to the secret's topic. *fcm.Client implements it; its
// SendMessage falls back to the small padding if FCM rejects a 2048-byte topic payload.
type Sender interface {
	SendMessage(ctx context.Context, secret proto.Secret, m proto.Message, rnd io.Reader) (string, proto.Message, error)
}

// NewSender builds the FCM client from config.toml's fcm_service_account and fcm_endpoint.
func NewSender(cfg *config.Config, home string, log *slog.Logger) (Sender, error) {
	return fcm.NewFromConfig(cfg, home, fcm.Options{Logger: log})
}

// SendMessage fills v/id/ts and sends m, sealed with the secret's key, to the secret's topic.
func SendMessage(ctx context.Context, s Sender, secret proto.Secret, m proto.Message) (string, error) {
	return sendAt(ctx, s, secret, m, time.Now())
}

func sendAt(ctx context.Context, s Sender, secret proto.Secret, m proto.Message, now time.Time) (string, error) {
	m.V = proto.Version
	if m.ID == "" {
		id, err := proto.NewID(nil)
		if err != nil {
			return "", err
		}
		m.ID = id
	}
	if m.TS == 0 {
		m.TS = now.Unix()
	}
	name, _, err := s.SendMessage(ctx, secret, m, nil)
	return name, err
}

// PairPings sends a pair message right away and then every interval until ctx ends. It returns the
// number of pings sent; send errors are logged and retried at the next tick.
func PairPings(ctx context.Context, s Sender, secret proto.Secret, interval time.Duration, log *slog.Logger) (int, error) {
	n := 0
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := SendMessage(ctx, s, secret, proto.Message{T: proto.TypePair}); err != nil {
			if ctx.Err() != nil {
				return n, ctx.Err()
			}
			log.Warn("pairing ping failed", "err", err)
		} else {
			n++
			log.Info("pairing ping sent", "count", n)
		}
		select {
		case <-ctx.Done():
			return n, ctx.Err()
		case <-t.C:
		}
	}
}
