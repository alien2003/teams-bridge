package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"io"
	"strings"
	"testing"
	"unicode/utf16"
)

var winEnv = envOf(map[string]string{"USERDOMAIN": "PC", "USERNAME": "alice"})

const goldenTaskXML = `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>tbd &lt;test&gt; &amp; "more"</Description>
    <URI>\tbd</URI>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>PC\alice</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>PC\alice</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>255</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%SystemRoot%\System32\conhost.exe</Command>
      <Arguments>--headless "C:\Program Files\tbd\tbd.exe" run --home "C:\Users\alice\tbd data\\" --log &amp;x</Arguments>
    </Exec>
  </Actions>
</Task>
`

var goldenTaskSpec = Spec{
	Exe:         `C:\Program Files\tbd\tbd.exe`,
	Home:        `C:\Users\alice\tbd data\`,
	Description: `tbd <test> & "more"`,
	ExtraArgs:   []string{"--log", "&x"},
}

func utf16le(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := []byte{0xFF, 0xFE}
	for _, v := range u {
		b = binary.LittleEndian.AppendUint16(b, v)
	}
	return b
}

func TestTaskXMLGolden(t *testing.T) {
	b, err := TaskXML(goldenTaskSpec, `PC\alice`)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b, []byte{0xFF, 0xFE, '<', 0, '?', 0}) {
		t.Fatalf("no UTF-16LE BOM + '<?': % x", b[:8])
	}
	want := utf16le(strings.ReplaceAll(goldenTaskXML, "\n", "\r\n"))
	if !bytes.Equal(b, want) {
		t.Errorf("XML mismatch\n--- got\n%s--- want\n%s", decodeText(b), goldenTaskXML)
	}
}

// TestTaskXMLStructure decodes the UTF-16 bytes independently and checks the structure with a
// namespace-aware encoding/xml schema of its own.
func TestTaskXMLStructure(t *testing.T) {
	b, err := TaskXML(goldenTaskSpec, `PC\alice`)
	if err != nil {
		t.Fatal(err)
	}
	if len(b)%2 != 0 {
		t.Fatal("odd length")
	}
	u := make([]uint16, (len(b)-2)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2+2*i:])
	}
	text := string(utf16.Decode(u))
	if !strings.HasPrefix(text, `<?xml version="1.0" encoding="UTF-16"?>`+"\r\n") {
		t.Errorf("declaration: %q", text[:50])
	}
	type exec struct {
		Command   string `xml:"Command"`
		Arguments string `xml:"Arguments"`
	}
	var doc struct {
		XMLName  xml.Name `xml:"http://schemas.microsoft.com/windows/2004/02/mit/task Task"`
		Version  string   `xml:"version,attr"`
		Triggers struct {
			Logon []struct {
				Enabled string `xml:"Enabled"`
				UserID  string `xml:"UserId"`
			} `xml:"http://schemas.microsoft.com/windows/2004/02/mit/task LogonTrigger"`
		} `xml:"Triggers"`
		Principal struct {
			ID        string `xml:"id,attr"`
			UserID    string `xml:"UserId"`
			LogonType string `xml:"LogonType"`
			RunLevel  string `xml:"RunLevel"`
		} `xml:"Principals>Principal"`
		Settings struct {
			MultipleInstancesPolicy    string
			DisallowStartIfOnBatteries string
			StopIfGoingOnBatteries     string
			ExecutionTimeLimit         string
			Enabled                    string
			Interval                   string `xml:"RestartOnFailure>Interval"`
			Count                      int    `xml:"RestartOnFailure>Count"`
		} `xml:"Settings"`
		Actions struct {
			Context string `xml:"Context,attr"`
			Exec    []exec `xml:"Exec"`
		} `xml:"Actions"`
	}
	dec := xml.NewDecoder(strings.NewReader(text))
	dec.CharsetReader = func(label string, r io.Reader) (io.Reader, error) {
		if label != "UTF-16" {
			t.Errorf("charset %q", label)
		}
		return r, nil
	}
	if err := dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != "1.2" || len(doc.Triggers.Logon) != 1 || doc.Triggers.Logon[0].UserID != `PC\alice` || doc.Triggers.Logon[0].Enabled != "true" {
		t.Errorf("trigger: %+v", doc)
	}
	if p := doc.Principal; p.ID != "Author" || p.UserID != `PC\alice` || p.LogonType != "InteractiveToken" || p.RunLevel != "LeastPrivilege" {
		t.Errorf("principal: %+v", p)
	}
	s := doc.Settings
	if s.MultipleInstancesPolicy != "IgnoreNew" || s.DisallowStartIfOnBatteries != "false" || s.StopIfGoingOnBatteries != "false" ||
		s.ExecutionTimeLimit != "PT0S" || s.Enabled != "true" || s.Interval != "PT1M" || s.Count != 255 {
		t.Errorf("settings: %+v", s)
	}
	if doc.Actions.Context != "Author" || len(doc.Actions.Exec) != 1 {
		t.Fatalf("actions: %+v", doc.Actions)
	}
	ex := doc.Actions.Exec[0]
	if ex.Command != TaskCommand {
		t.Errorf("command %q", ex.Command)
	}
	// The arguments, parsed the way conhost/CommandLineToArgvW parse them, are exactly the daemon's argv.
	got := parseWinArgs(ex.Arguments)
	want := []string{"--headless", goldenTaskSpec.Exe, "run", "--home", goldenTaskSpec.Home, "--log", "&x"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("arguments parse to %q, want %q", got, want)
	}

	info, err := ParseTaskXML(b)
	if err != nil {
		t.Fatal(err)
	}
	if info.Command != TaskCommand || info.Arguments != ex.Arguments || info.LogonUserID != `PC\alice` ||
		info.ExecutionTimeLimit != "PT0S" || info.RestartCount != 255 || info.URI != `\tbd` || info.Description != goldenTaskSpec.Description {
		t.Errorf("ParseTaskXML: %+v", info)
	}
}

func TestTaskXMLRejects(t *testing.T) {
	for _, s := range []Spec{
		{Exe: `tbd.exe`, Home: `C:\h`},
		{Exe: `/usr/bin/tbd`, Home: `C:\h`},
		{Exe: `C:\tbd.exe`, Home: `h`},
		{Exe: `C:\%TEMP%\tbd.exe`},
		{Exe: `C:\tbd.exe`, ExtraArgs: []string{"%PATH%"}},
		{Exe: `C:\tbd.exe`, Description: "a\x00b"},
		{Exe: `C:\tbd.exe`, Name: `a\b`},
	} {
		if _, err := TaskXML(s, `PC\u`); err == nil {
			t.Errorf("TaskXML(%+v): no error", s)
		}
	}
	if _, err := TaskXML(Spec{Exe: `C:\tbd.exe`}, ""); err == nil {
		t.Error("empty user: no error")
	}
	if _, err := TaskXML(Spec{Exe: `\\server\share\tbd.exe`}, "u"); err != nil {
		t.Errorf("UNC exe: %v", err)
	}
}

func TestCurrentUserID(t *testing.T) {
	if u, err := CurrentUserID(winEnv.Getenv); u != `PC\alice` || err != nil {
		t.Errorf("%q %v", u, err)
	}
	if u, err := CurrentUserID(envOf(map[string]string{"USERNAME": "bob"}).Getenv); u != "bob" || err != nil {
		t.Errorf("%q %v", u, err)
	}
	if _, err := CurrentUserID(envOf(nil).Getenv); err == nil {
		t.Error("no USERNAME: no error")
	}
	if _, err := CurrentUserID(nil); err == nil {
		t.Error("nil getenv: no error")
	}
}

// parseWinArgs splits a command-line tail the way CommandLineToArgvW and the MSVC runtime do.
func parseWinArgs(s string) []string {
	var args []string
	var cur strings.Builder
	inArg, inQuote := false, false
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case (c == ' ' || c == '\t') && !inQuote:
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
			i++
		case c == '\\':
			inArg = true
			n := 0
			for i < len(s) && s[i] == '\\' {
				n++
				i++
			}
			if i < len(s) && s[i] == '"' {
				cur.WriteString(strings.Repeat(`\`, n/2))
				if n%2 == 1 {
					cur.WriteByte('"')
					i++
				}
			} else {
				cur.WriteString(strings.Repeat(`\`, n))
			}
		case c == '"':
			inArg = true
			if inQuote && i+1 < len(s) && s[i+1] == '"' {
				cur.WriteByte('"')
				i += 2
				continue
			}
			inQuote = !inQuote
			i++
		default:
			inArg = true
			cur.WriteByte(c)
			i++
		}
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args
}

var winQuoteCases = []string{
	"", "plain", `C:\Program Files\tbd\tbd.exe`, `C:\dir\`, `C:\dir with space\`, `C:\a\\b`,
	`a"b`, `a\"b`, `a\\"b`, `"`, `\`, `\\`, "tab\there", `ends with quote"`, `x \\ y\\`, "ünï cødé",
}

func TestWinQuoteRoundTrip(t *testing.T) {
	for _, s := range winQuoteCases {
		for _, force := range []bool{false, true} {
			q := winQuote(s, force)
			got := parseWinArgs("--a " + q + " --b")
			if len(got) != 3 || got[1] != s {
				t.Errorf("winQuote(%q, %v) = %s parses to %q", s, force, q, got)
			}
		}
	}
	for in, want := range map[string]string{
		"plain":                `plain`,
		`C:\x\y`:               `C:\x\y`,
		`C:\Program Files\x`:   `"C:\Program Files\x"`,
		`C:\dir with space\`:   `"C:\dir with space\\"`,
		`a"b`:                  `"a\"b"`,
		"":                     `""`,
		`C:\Users\a\AppData\x`: `C:\Users\a\AppData\x`,
	} {
		if got := winQuote(in, false); got != want {
			t.Errorf("winQuote(%q) = %s, want %s", in, got, want)
		}
	}
	if got := winQuote(`C:\tbd\tbd.exe`, true); got != `"C:\tbd\tbd.exe"` {
		t.Errorf("forced: %s", got)
	}
}

func TestShQuote(t *testing.T) {
	for in, want := range map[string]string{
		"systemctl":            "systemctl",
		"--user":               "--user",
		"/home/u/a b/x":        `'/home/u/a b/x'`,
		"it's":                 `'it'\''s'`,
		"":                     "''",
		"$HOME":                `'$HOME'`,
		"tbd-test-1.service":   "tbd-test-1.service",
		"/x/.config/systemd/u": "/x/.config/systemd/u",
	} {
		if got := shQuote(in); got != want {
			t.Errorf("shQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

// A definition as `schtasks /query /tn X /xml` prints it: declaration, a comment naming the task,
// settings in Windows' own order with defaults spelled out, the principal as a SID.
const exportedTask = `<?xml version="1.0" encoding="UTF-16"?>
<!-- \tbd-test-1 -->
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <URI>\tbd-test-1</URI>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>PC\alice</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>S-1-5-21-1111111111-2222222222-3333333333-1001</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>false</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>true</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>255</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%SystemRoot%\System32\conhost.exe</Command>
      <Arguments>--headless "C:\Windows\System32\whoami.exe" run --home "C:\sandbox\home dir"</Arguments>
    </Exec>
  </Actions>
</Task>
`

func TestParseTaskXMLExport(t *testing.T) {
	check := func(name string, b []byte) {
		t.Helper()
		info, err := ParseTaskXML(b)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if info.URI != `\tbd-test-1` || !info.HasLogonTrigger || info.LogonUserID != `PC\alice` ||
			!strings.HasPrefix(info.PrincipalUserID, "S-1-5-21-") || info.LogonType != "InteractiveToken" || info.RunLevel != "LeastPrivilege" ||
			info.MultipleInstancesPolicy != "IgnoreNew" || info.ExecutionTimeLimit != "PT0S" || info.Enabled != "true" ||
			info.RestartInterval != "PT1M" || info.RestartCount != 255 || info.ExecCount != 1 ||
			info.Command != TaskCommand || info.Arguments != `--headless "C:\Windows\System32\whoami.exe" run --home "C:\sandbox\home dir"` {
			t.Errorf("%s: %+v", name, info)
		}
	}
	crlf := strings.ReplaceAll(exportedTask, "\n", "\r\n")
	check("utf-16 with BOM", utf16le(crlf))
	check("utf-16 without BOM", utf16le(crlf)[2:])
	check("8-bit", []byte(crlf))
	check("utf-8 BOM", append([]byte{0xEF, 0xBB, 0xBF}, crlf...))
	check("wrapped", []byte("<Tasks>"+strings.Replace(crlf, `<?xml version="1.0" encoding="UTF-16"?>`, "", 1)+"</Tasks>"))
	if _, err := ParseTaskXML([]byte("ERROR: The system cannot find the file specified.\r\n")); err == nil {
		t.Error("error text parsed as a task")
	}
	if _, err := ParseTaskXML([]byte(`<?xml version="1.0"?><Task><Actions>`)); err == nil {
		t.Error("truncated XML parsed")
	}
}

func TestQueryTask(t *testing.T) {
	r := &fakeRunner{
		out: map[string]string{
			"schtasks /query /tn tbd-test-1 /xml": string(utf16le(exportedTask)),
			"schtasks /query /tn gone /xml":       "ERROR: The system cannot find the file specified.\r\n",
		},
		fail: map[string]bool{"schtasks /query /tn gone /xml": true},
	}
	info, err := QueryTask(context.Background(), r, "tbd-test-1")
	if err != nil || info.Command != TaskCommand {
		t.Errorf("%+v %v", info, err)
	}
	if _, err := QueryTask(context.Background(), r, "gone"); err == nil || !strings.Contains(err.Error(), "cannot find") {
		t.Errorf("gone: %v", err)
	}
}

func TestWindowsPlans(t *testing.T) {
	spec := Spec{Exe: `C:\Users\alice\bin\tbd.exe`, Home: `C:\Users\alice\AppData\Local\tbd`}
	p, err := InstallPlan(spec, "windows", winEnv)
	if err != nil {
		t.Fatal(err)
	}
	xmlFile := `C:\Users\alice\AppData\Local\tbd\tbd.task.xml`
	if p.OS != "windows" || len(p.Files) != 1 || p.Files[0].Path != xmlFile {
		t.Fatalf("files: %+v", p.Files)
	}
	want, _ := TaskXML(spec, `PC\alice`)
	if !bytes.Equal(p.Files[0].Content, want) {
		t.Error("content is not TaskXML(spec)")
	}
	equalLines(t, "install", cmdLines(p.Commands), []string{
		"schtasks /create /tn tbd /xml " + xmlFile + " /f",
		"schtasks /run /tn tbd",
	})
	for _, c := range p.Commands {
		if strings.EqualFold(c[1], "/sc") || strings.Contains(strings.Join(c, " "), "onlogon") {
			t.Errorf("uses /sc onlogon, which needs elevation: %v", c)
		}
	}
	spec.NoStart = true
	p, _ = InstallPlan(spec, "windows", winEnv)
	equalLines(t, "no-start", cmdLines(p.Commands), []string{"schtasks /create /tn tbd /xml " + xmlFile + " /f"})

	u, err := UninstallPlan(Spec{Home: spec.Home + `\`}, "windows", Env{})
	if err != nil {
		t.Fatal(err)
	}
	equalLines(t, "uninstall", cmdLines(u.Commands), []string{"schtasks /delete /tn tbd /f"})
	equalLines(t, "uninstall removes", u.RemoveFiles, []string{xmlFile})
	if !u.BestEffort {
		t.Error("uninstall not best effort")
	}

	if _, err := InstallPlan(Spec{Exe: spec.Exe}, "windows", winEnv); err == nil {
		t.Error("no Home: no error")
	}
	if _, err := InstallPlan(spec, "windows", envOf(nil)); err == nil {
		t.Error("no USERNAME: no error")
	}
	if _, err := InstallPlan(spec, "darwin", winEnv); err == nil {
		t.Error("darwin: no error")
	}
}

func TestDecodeText(t *testing.T) {
	for name, b := range map[string][]byte{
		"le bom":  utf16le("<a>ü</a>"),
		"le":      utf16le("<a>ü</a>")[2:],
		"be bom":  {0xFE, 0xFF, 0, '<', 0, 'a', 0, '>', 0, 0xFC, 0, '<', 0, '/', 0, 'a', 0, '>'},
		"utf8":    []byte("<a>ü</a>"),
		"utf8bom": append([]byte{0xEF, 0xBB, 0xBF}, "<a>ü</a>"...),
	} {
		if got := decodeText(b); got != "<a>ü</a>" {
			t.Errorf("%s: %q", name, got)
		}
	}
	if got := decodeText([]byte{'a', 0xFF, 'b'}); got != "a\uFFFDb" {
		t.Errorf("invalid: %q", got)
	}
}
