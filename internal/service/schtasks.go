package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// TaskCommand is the task's executable: conhost.exe --headless runs tbd.exe with no console window
// (DECISIONS D5). Task Scheduler expands %SystemRoot% when it starts the task; the full path keeps
// the lookup independent of PATH.
const TaskCommand = `%SystemRoot%\System32\conhost.exe`

// TaskNamespace is the Task Scheduler XML namespace.
const TaskNamespace = "http://schemas.microsoft.com/windows/2004/02/mit/task"

// TaskRestartCount is RestartOnFailure/Count: an xs:unsignedByte in the schema, so 255 is the most.
const TaskRestartCount = 255

// CurrentUserID is DOMAIN\user of the current Windows user, from USERDOMAIN and USERNAME.
func CurrentUserID(getenv func(string) string) (string, error) {
	if getenv == nil {
		return "", errors.New("service: no environment to read USERNAME from")
	}
	user := getenv("USERNAME")
	if user == "" || hasControl(user) {
		return "", errors.New("service: USERNAME is not set; cannot name the task's user")
	}
	if d := getenv("USERDOMAIN"); d != "" && !hasControl(d) {
		return d + `\` + user, nil
	}
	return user, nil
}

// TaskArguments is the Arguments of the task's Exec action:
// --headless "<exe>" run [--home "<home>"] [extra args...], quoted for the Windows command line.
func TaskArguments(spec Spec) string {
	parts := []string{"--headless", winQuote(spec.Exe, true)}
	for _, a := range spec.Args() {
		parts = append(parts, winQuote(a, false))
	}
	return strings.Join(parts, " ")
}

// TaskXML renders the Task Scheduler definition for spec as UTF-16LE with a BOM, which is what
// `schtasks /create /xml` expects. Trigger and principal name userID (DOMAIN\user): a logon trigger
// for one's own account registers without elevation, one for any user does not (DECISIONS D5).
func TaskXML(spec Spec, userID string) ([]byte, error) {
	s, err := spec.normalize("windows", true)
	if err != nil {
		return nil, err
	}
	if userID == "" || hasControl(userID) {
		return nil, fmt.Errorf("service: invalid task user %q", userID)
	}
	return encodeUTF16LE(taskXMLText(s, userID)), nil
}

func taskXMLText(s Spec, userID string) string {
	e := xmlEscape
	lines := []string{
		`<?xml version="1.0" encoding="UTF-16"?>`,
		`<Task version="1.2" xmlns="` + TaskNamespace + `">`,
		`  <RegistrationInfo>`,
		`    <Description>` + e(s.Description) + `</Description>`,
		`    <URI>\` + e(s.Name) + `</URI>`,
		`  </RegistrationInfo>`,
		`  <Triggers>`,
		`    <LogonTrigger>`,
		`      <Enabled>true</Enabled>`,
		`      <UserId>` + e(userID) + `</UserId>`,
		`    </LogonTrigger>`,
		`  </Triggers>`,
		`  <Principals>`,
		`    <Principal id="Author">`,
		`      <UserId>` + e(userID) + `</UserId>`,
		`      <LogonType>InteractiveToken</LogonType>`,
		`      <RunLevel>LeastPrivilege</RunLevel>`,
		`    </Principal>`,
		`  </Principals>`,
		`  <Settings>`,
		`    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>`,
		`    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>`,
		`    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>`,
		`    <AllowStartOnDemand>true</AllowStartOnDemand>`,
		`    <Enabled>true</Enabled>`,
		// The default limit is 72 h, which would kill the daemon after three days.
		`    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>`,
		`    <RestartOnFailure>`,
		`      <Interval>PT1M</Interval>`,
		`      <Count>` + strconv.Itoa(TaskRestartCount) + `</Count>`,
		`    </RestartOnFailure>`,
		`  </Settings>`,
		`  <Actions Context="Author">`,
		`    <Exec>`,
		`      <Command>` + e(TaskCommand) + `</Command>`,
		`      <Arguments>` + e(TaskArguments(s)) + `</Arguments>`,
		`    </Exec>`,
		`  </Actions>`,
		`</Task>`,
	}
	return strings.Join(lines, "\r\n") + "\r\n"
}

// xmlEscape escapes character data. Quotes stay literal (valid in text), so the arguments read as
// they will run; control characters were rejected by normalize.
func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func encodeUTF16LE(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2+2*len(u))
	b[0], b[1] = 0xFF, 0xFE
	for i, v := range u {
		binary.LittleEndian.PutUint16(b[2+2*i:], v)
	}
	return b
}

// isUTF16LEBOM reports whether b starts with the UTF-16LE byte order mark.
func isUTF16LEBOM(b []byte) bool { return len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE }

// decodeText turns tool output or a file into a Go string: UTF-16 (with a BOM, or LE without one,
// recognised by NUL high bytes) or UTF-8 / an 8-bit code page, invalid bytes replaced.
func decodeText(b []byte) string {
	le := true
	switch {
	case isUTF16LEBOM(b):
		b = b[2:]
	case len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF:
		b, le = b[2:], false
	case len(b) >= 4 && b[0] != 0 && b[1] == 0 && b[2] != 0 && b[3] == 0:
	default:
		return strings.ToValidUTF8(string(bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})), string(utf8.RuneError))
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		if le {
			u[i] = binary.LittleEndian.Uint16(b[2*i:])
		} else {
			u[i] = binary.BigEndian.Uint16(b[2*i:])
		}
	}
	return string(utf16.Decode(u))
}

// TaskInfo is what ParseTaskXML extracts from a task definition.
type TaskInfo struct {
	URI         string
	Description string
	// LogonTrigger: whether there is one, and the user it is limited to ("" = any user).
	HasLogonTrigger bool
	LogonUserID     string
	PrincipalUserID string
	LogonType       string
	RunLevel        string
	// Settings, verbatim ("" = element absent, i.e. the schema default).
	MultipleInstancesPolicy    string
	DisallowStartIfOnBatteries string
	StopIfGoingOnBatteries     string
	ExecutionTimeLimit         string
	Enabled                    string
	RestartInterval            string
	RestartCount               int
	// First Exec action.
	Command   string
	Arguments string
	ExecCount int
}

type taskDoc struct {
	RegistrationInfo struct {
		Description string `xml:"Description"`
		URI         string `xml:"URI"`
	} `xml:"RegistrationInfo"`
	Triggers struct {
		Logon []struct {
			Enabled string `xml:"Enabled"`
			UserID  string `xml:"UserId"`
		} `xml:"LogonTrigger"`
	} `xml:"Triggers"`
	Principals struct {
		Principal []struct {
			ID        string `xml:"id,attr"`
			UserID    string `xml:"UserId"`
			LogonType string `xml:"LogonType"`
			RunLevel  string `xml:"RunLevel"`
		} `xml:"Principal"`
	} `xml:"Principals"`
	Settings struct {
		MultipleInstancesPolicy    string `xml:"MultipleInstancesPolicy"`
		DisallowStartIfOnBatteries string `xml:"DisallowStartIfOnBatteries"`
		StopIfGoingOnBatteries     string `xml:"StopIfGoingOnBatteries"`
		ExecutionTimeLimit         string `xml:"ExecutionTimeLimit"`
		Enabled                    string `xml:"Enabled"`
		RestartOnFailure           struct {
			Interval string `xml:"Interval"`
			Count    string `xml:"Count"`
		} `xml:"RestartOnFailure"`
	} `xml:"Settings"`
	Actions struct {
		Exec []struct {
			Command   string `xml:"Command"`
			Arguments string `xml:"Arguments"`
		} `xml:"Exec"`
	} `xml:"Actions"`
}

// ErrNoTask means the input has no <Task> element.
var ErrNoTask = errors.New("service: no <Task> element")

// ParseTaskXML parses a task definition: a file written by TaskXML or the output of
// `schtasks /query /tn <name> /xml` (UTF-16 or 8-bit, possibly with comments or a <Tasks> wrapper).
func ParseTaskXML(b []byte) (TaskInfo, error) {
	dec := xml.NewDecoder(strings.NewReader(decodeText(b)))
	// The text is already decoded; the declaration's encoding="UTF-16" no longer applies.
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return TaskInfo{}, ErrNoTask
		}
		if err != nil {
			return TaskInfo{}, fmt.Errorf("service: task XML: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "Task" {
			continue
		}
		var d taskDoc
		if err := dec.DecodeElement(&d, &se); err != nil {
			return TaskInfo{}, fmt.Errorf("service: task XML: %w", err)
		}
		return d.info(), nil
	}
}

func (d *taskDoc) info() TaskInfo {
	t := TaskInfo{
		URI:                        strings.TrimSpace(d.RegistrationInfo.URI),
		Description:                d.RegistrationInfo.Description,
		MultipleInstancesPolicy:    strings.TrimSpace(d.Settings.MultipleInstancesPolicy),
		DisallowStartIfOnBatteries: strings.TrimSpace(d.Settings.DisallowStartIfOnBatteries),
		StopIfGoingOnBatteries:     strings.TrimSpace(d.Settings.StopIfGoingOnBatteries),
		ExecutionTimeLimit:         strings.TrimSpace(d.Settings.ExecutionTimeLimit),
		Enabled:                    strings.TrimSpace(d.Settings.Enabled),
		RestartInterval:            strings.TrimSpace(d.Settings.RestartOnFailure.Interval),
		ExecCount:                  len(d.Actions.Exec),
	}
	t.RestartCount, _ = strconv.Atoi(strings.TrimSpace(d.Settings.RestartOnFailure.Count))
	if len(d.Triggers.Logon) > 0 {
		t.HasLogonTrigger = true
		t.LogonUserID = strings.TrimSpace(d.Triggers.Logon[0].UserID)
	}
	if len(d.Principals.Principal) > 0 {
		p := d.Principals.Principal[0]
		t.PrincipalUserID = strings.TrimSpace(p.UserID)
		t.LogonType = strings.TrimSpace(p.LogonType)
		t.RunLevel = strings.TrimSpace(p.RunLevel)
	}
	if len(d.Actions.Exec) > 0 {
		t.Command = strings.TrimSpace(d.Actions.Exec[0].Command)
		t.Arguments = strings.TrimSpace(d.Actions.Exec[0].Arguments)
	}
	return t
}

// QueryTask runs `schtasks /query /tn <name> /xml` and parses the definition. It fails when the task
// does not exist (schtasks exits non-zero).
func QueryTask(ctx context.Context, r Runner, name string) (TaskInfo, error) {
	if r == nil {
		r = ExecRunner{}
	}
	out, err := r.Run(ctx, "schtasks", "/query", "/tn", name, "/xml")
	if err != nil {
		return TaskInfo{}, fmt.Errorf("schtasks /query /tn %s: %w: %s", name, err, strings.TrimSpace(decodeText([]byte(out))))
	}
	return ParseTaskXML([]byte(out))
}

func schtasks(args ...string) []string { return append([]string{"schtasks"}, args...) }

// TaskFile is where the install keeps the task XML: <Home>\<name>.task.xml.
func TaskFile(spec Spec) string {
	name := spec.Name
	if name == "" {
		name = DefaultName
	}
	if spec.Home == "" {
		return ""
	}
	return strings.TrimRight(spec.Home, `\/`) + `\` + name + ".task.xml"
}

func windowsInstall(s Spec, env Env) (Plan, error) {
	if s.Home == "" {
		return Plan{}, errors.New("service: Home is required on Windows (the task XML is kept there)")
	}
	user, err := CurrentUserID(env.Getenv)
	if err != nil {
		return Plan{}, err
	}
	x, err := TaskXML(s, user)
	if err != nil {
		return Plan{}, err
	}
	file := TaskFile(s)
	p := Plan{
		OS:       "windows",
		Files:    []File{{Path: file, Content: x, Mode: 0o644}},
		Commands: [][]string{schtasks("/create", "/tn", s.Name, "/xml", file, "/f")},
	}
	note := fmt.Sprintf("Task %s starts tbd hidden (conhost --headless) at every logon of %s", s.Name, user)
	if s.NoStart {
		note += fmt.Sprintf("; install does not start it now (schtasks /run /tn %s)", s.Name)
	} else {
		p.Commands = append(p.Commands, schtasks("/run", "/tn", s.Name))
		note += "; install also starts it now"
	}
	// Not `schtasks /end`: it ends conhost.exe only, and tbd.exe (before it watched its console
	// host) kept running (DECISIONS D20).
	note += fmt.Sprintf(".\nStatus: schtasks /query /tn %s /v /fo list; stop: tbd stop; start again: tbd start", s.Name)
	p.Notes = []string{note}
	return p, nil
}

func windowsUninstall(s Spec) Plan {
	p := Plan{OS: "windows", Commands: [][]string{schtasks("/delete", "/tn", s.Name, "/f")}, BestEffort: true}
	if f := TaskFile(s); f != "" {
		p.RemoveFiles = []string{f}
	}
	p.Notes = []string{"Deleting the task does not stop a tbd.exe that is already running: stop it with `tbd stop`."}
	return p
}
