package daemon

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"time"

	"github.com/alien2003/teams-bridge/internal/config"
	"github.com/alien2003/teams-bridge/internal/fcm"
	"github.com/alien2003/teams-bridge/internal/proto"
	"github.com/alien2003/teams-bridge/internal/service"
)

// StatusOptions configure PrintStatus.
type StatusOptions struct {
	ServiceName string         // default service.DefaultName
	Runner      service.Runner // nil → service.ExecRunner

	goos string       // test hook: default runtime.GOOS
	env  *service.Env // test hook: default service.DefaultEnv()
}

// PrintStatus prints what `tbd status` shows. It never prints the secret or the service-account key.
func PrintStatus(ctx context.Context, w io.Writer, home string, cfg *config.Config, version string, o StatusOptions) error {
	p := func(k, f string, a ...any) { fmt.Fprintf(w, "%-16s %s\n", k+":", fmt.Sprintf(f, a...)) }
	p("tbd", "%s (protocol v%d)", version, proto.Version)
	p("data directory", "%s", home)
	p("config", "%s", config.Path(home))
	p("teams url", "%s", cfg.TeamsURL)
	if s, err := cfg.PairingSecret(); err == nil {
		t := s.Topic()
		p("paired", "yes (topic %s…)", t[:6])
	} else {
		p("paired", "no — run `tbd pair`")
	}
	if raw, err := cfg.ServiceAccountJSON(home); err != nil {
		p("fcm", "not configured (%v)", err)
	} else if sa, err := fcm.ParseServiceAccount(raw); err != nil {
		p("fcm", "invalid service account: %v", err)
	} else {
		p("fcm", "project %s as %s", sa.ProjectID, sa.ClientEmail)
	}
	if b, err := cfg.DetectBrowser(); err != nil {
		p("browser", "%v", err)
	} else {
		v, verr := config.BrowserVersion(ctx, b.Path)
		ver := v.Full
		if verr != nil {
			ver = "version unknown: " + verr.Error()
		}
		p("browser", "%s (%s, via %s)", b.Path, ver, b.Source)
	}
	if held, pid := Held(home); held {
		p("daemon", "running (pid %s)", pid)
	} else {
		p("daemon", "not running")
	}
	if goos := o.goos; goos == "darwin" || goos == "" && runtime.GOOS == "darwin" {
		name, r, env := o.ServiceName, o.Runner, service.DefaultEnv()
		if name == "" {
			name = service.DefaultName
		}
		if r == nil {
			r = service.ExecRunner{}
		}
		if o.env != nil {
			env = *o.env
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		p("launchd", "%s", service.LaunchdStatus(cctx, r, name, env))
		cancel()
	}
	st := LoadState(home)
	ago := func(t time.Time) string {
		if t.IsZero() {
			return "never"
		}
		return fmt.Sprintf("%s (%s ago)", t.Local().Format(time.DateTime), time.Since(t).Round(time.Second))
	}
	p("last heartbeat", "%s", ago(st.LastHeartbeat))
	p("last message", "%s (%d sent in total)", ago(st.LastMessage), st.Messages)
	p("last capture", "%s", ago(st.LastEvent))
	if !st.StartedAt.IsZero() {
		session := "ok"
		if !st.SessionOK {
			session = "expired or unknown — run `tbd login` if alerts say so"
		}
		p("session", "%s", session)
		p("unread (title)", "%d", st.UnreadCount)
	}
	for k, t := range st.LastAlerts {
		p("alert "+k, "%s", ago(t))
	}
	if st.LastError != "" {
		p("last error", "%s", st.LastError)
	}
	return nil
}
