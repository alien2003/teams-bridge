package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/alien2003/teams-bridge/internal/config"
	"github.com/alien2003/teams-bridge/internal/daemon"
	"github.com/alien2003/teams-bridge/internal/fcm/fcmtest"
	"github.com/alien2003/teams-bridge/internal/proto"
)

func runCLI(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

func TestUsage(t *testing.T) {
	if code, _, e := runCLI(t, ""); code != 2 || !strings.Contains(e, "Usage:") {
		t.Errorf("no args: %d %q", code, e)
	}
	if code, _, e := runCLI(t, "", "frobnicate"); code != 2 || !strings.Contains(e, "unknown command") {
		t.Errorf("unknown: %d %q", code, e)
	}
	if code, _, e := runCLI(t, "", "run", "--bogus"); code != 2 && code != 1 || e == "" {
		t.Errorf("bad flag: %d %q", code, e)
	}
	if code, out, _ := runCLI(t, "", "version"); code != 0 || !strings.Contains(out, "protocol v1") {
		t.Errorf("version: %d %q", code, out)
	}
	if code, _, e := runCLI(t, "", "--home", t.TempDir(), "status", "extra"); code != 2 || !strings.Contains(e, "unexpected argument") {
		t.Errorf("extra arg: %d %q", code, e)
	}
}

var displayCode = regexp.MustCompile(`[A-Z2-7]{4}(-[A-Z2-7]{4}){5}-[A-Z2-7]{2}`)

func TestPairCreatesShowsAndRotates(t *testing.T) {
	home := t.TempDir()
	code, out, e := runCLI(t, "", "pair", "--home", home, "--no-ping")
	if code != 0 {
		t.Fatalf("pair: %d %s", code, e)
	}
	shown := displayCode.FindString(out)
	s, err := proto.ParseCode(shown)
	if err != nil {
		t.Fatalf("printed code %q: %v\n%s", shown, err, out)
	}
	if !strings.Contains(out, "█") && !strings.Contains(out, "▀") {
		t.Error("no QR code printed")
	}
	cfg, _, err := config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := cfg.PairingSecret(); got != s {
		t.Fatal("printed code differs from the saved secret")
	}
	// Again: the same secret is shown.
	_, out2, _ := runCLI(t, "", "--home", home, "pair", "--no-ping", "--ascii")
	if displayCode.FindString(out2) != shown || !strings.Contains(out2, "Already paired") || !strings.Contains(out2, "##") {
		t.Fatalf("second pair did not show the existing code:\n%s", out2)
	}
	// --new rotates.
	_, out3, _ := runCLI(t, "", "--home", home, "pair", "--no-ping", "--new")
	if c := displayCode.FindString(out3); c == "" || c == shown {
		t.Fatalf("--new did not rotate: %q", c)
	}
	// status never prints the secret.
	cfg, _, _ = config.Load(home)
	_, st, _ := runCLI(t, "", "--home", home, "status")
	if strings.Contains(st, cfg.Secret) || displayCode.MatchString(st) {
		t.Fatalf("status leaks the secret:\n%s", st)
	}
	if !strings.Contains(st, "paired:") || !strings.Contains(st, "yes") {
		t.Fatalf("status:\n%s", st)
	}
}

func TestPairPingsAndTestPush(t *testing.T) {
	srv := fcmtest.NewServer(t, fcmtest.WithSecret(proto.VectorSecret))
	home := t.TempDir()
	cfg := config.Default()
	cfg.Secret = proto.VectorSecret.Code()
	srv.SetupHome(home, cfg)

	code, out, e := runCLI(t, "", "--home", home, "pair", "--ping-for", "3s")
	if code != 0 || !strings.Contains(out, "Sent 1 pairing ping") {
		t.Fatalf("pair: %d\n%s\n%s", code, out, e)
	}
	if code, out, e := runCLI(t, "", "--home", home, "test-push"); code != 0 || !strings.Contains(out, "Sent") {
		t.Fatalf("test-push: %d %s %s", code, out, e)
	}
	msgs := srv.MustWaitFor(2, 10*time.Second)
	types := map[string]bool{}
	for _, m := range msgs {
		if m.DecryptErr != nil || m.Decrypted == nil {
			t.Fatalf("undecryptable: %v", m.DecryptErr)
		}
		types[m.Decrypted.T] = true
	}
	if !types[proto.TypePair] || !types[proto.TypeMsg] {
		t.Fatalf("got types %v", types)
	}
}

func TestTestPushNeedsPairing(t *testing.T) {
	home := t.TempDir()
	if code, _, e := runCLI(t, "", "--home", home, "test-push"); code != 1 || !strings.Contains(e, "not paired") {
		t.Fatalf("%d %q", code, e)
	}
}

func TestInstallDryRunTouchesNothing(t *testing.T) {
	home := t.TempDir()
	code, out, e := runCLI(t, "", "--home", home, "install", "--dry-run")
	if code != 0 {
		t.Fatalf("install --dry-run: %d %s", code, e)
	}
	if !strings.Contains(out, home) || !strings.Contains(out, " run ") && !strings.Contains(out, " run\n") && !strings.Contains(out, "run --home") {
		t.Fatalf("dry run does not show the service command line:\n%s", out)
	}
	if ents, _ := os.ReadDir(home); len(ents) != 0 {
		t.Fatalf("dry run created files: %v", ents)
	}
	if code, _, e := runCLI(t, "", "--home", home, "uninstall", "--dry-run"); code != 0 {
		t.Fatalf("uninstall --dry-run: %d %s", code, e)
	}
}

func TestLogFileRotation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x", "tbd.log")
	f, err := openLogFile(p)
	if err != nil {
		t.Fatal(err)
	}
	chunk := bytes.Repeat([]byte("x"), 1<<20)
	for range 12 { // rotates while writing, not only at the next start
		if _, err := f.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	if st, err := os.Stat(p + ".1"); err != nil || st.Size() > maxLogSize {
		t.Fatalf("not rotated while running: %v", err)
	}
	if st, err := os.Stat(p); err != nil || st.Size() > maxLogSize || st.Size() == 0 {
		t.Fatalf("current log: %v", err)
	}
}

func TestRunErrorReachesLogFile(t *testing.T) {
	home := t.TempDir()
	logf := filepath.Join(home, "tbd.log")
	if code, _, _ := runCLI(t, "", "--home", home, "run", "--log-file", logf); code == 0 {
		t.Fatal("run without pairing succeeded")
	}
	b, err := os.ReadFile(logf)
	if err != nil || !strings.Contains(string(b), "not paired") {
		t.Fatalf("log file lacks the fatal error: %v %q", err, b)
	}
}

func TestPairNewRefusedWhileDaemonRuns(t *testing.T) {
	home := t.TempDir()
	if code, _, e := runCLI(t, "", "--home", home, "pair", "--no-ping"); code != 0 {
		t.Fatal(e)
	}
	before, _, _ := config.Load(home)
	lock, err := daemon.AcquireLock(home)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	code, _, e := runCLI(t, "", "--home", home, "pair", "--no-ping", "--new")
	if code == 0 || !strings.Contains(e, "is running") || !strings.Contains(e, "run `tbd stop`") {
		t.Fatalf("pair --new while the daemon runs: %d %q", code, e)
	}
	after, _, _ := config.Load(home)
	if after.Secret != before.Secret {
		t.Fatal("secret changed although refused")
	}
}

func TestStopWithoutDaemon(t *testing.T) {
	home := t.TempDir()
	if code, out, e := runCLI(t, "", "stop", "--home", home); code != 0 || !strings.Contains(out, "not running") {
		t.Fatalf("stop: %d %q %q", code, out, e)
	}
	t.Setenv("TBD_HOME", home)
	if code, out, e := runCLI(t, "", "stop", "--timeout", "5s"); code != 0 || !strings.Contains(out, "not running") || !strings.Contains(out, home) {
		t.Fatalf("stop with TBD_HOME: %d %q %q", code, out, e)
	}
	if code, _, e := runCLI(t, "", "stop", "--timeout", "0s"); code != 2 || !strings.Contains(e, "--timeout") {
		t.Fatalf("stop --timeout 0: %d %q", code, e)
	}
}

func TestStartWhileRunning(t *testing.T) {
	home := t.TempDir()
	lock, err := daemon.AcquireLock(home)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if code, out, e := runCLI(t, "", "--home", home, "start"); code != 0 || !strings.Contains(out, "already running") {
		t.Fatalf("start: %d %q %q", code, out, e)
	}
}

// Every command that needs the data directory names the fix when the daemon holds it.
func TestLockedCommandsNameTheFix(t *testing.T) {
	srv := fcmtest.NewServer(t, fcmtest.WithSecret(proto.VectorSecret))
	home := t.TempDir()
	cfg := config.Default()
	cfg.Secret = proto.VectorSecret.Code()
	srv.SetupHome(home, cfg)
	lock, err := daemon.AcquireLock(home)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	for _, args := range [][]string{{"login", "--timeout", "5s"}, {"run", "--timeout", "5s"}, {"pair", "--new", "--no-ping"}} {
		code, _, e := runCLI(t, "", append([]string{"--home", home}, args...)...)
		if code != 1 || !strings.Contains(e, "run `tbd stop`") || !strings.Contains(e, "`tbd start`") || !strings.Contains(e, "pid ") {
			t.Errorf("%v: %d %q", args, code, e)
		}
	}
}
