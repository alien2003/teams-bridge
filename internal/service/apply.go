package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Runner runs a command and returns its combined output.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (output string, err error)
}

// ExecRunner runs commands with os/exec.
type ExecRunner struct{}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// Apply executes p, or with dryRun prints exactly what would be created, written (path, mode and
// full content), unloaded and run (one quoted line per command), touching nothing and running
// nothing. Notes are
// printed in both modes. Without BestEffort the first failure stops Apply; a failed command's error
// carries its output.
func Apply(ctx context.Context, p Plan, r Runner, out io.Writer, dryRun bool) error {
	if out == nil {
		out = io.Discard
	}
	if dryRun {
		_, err := io.WriteString(out, DryRunText(p))
		return err
	}
	if r == nil {
		r = ExecRunner{}
	}
	var errs []error
	// fail records err and reports whether Apply must stop.
	fail := func(err error) bool {
		errs = append(errs, err)
		return !p.BestEffort
	}
	for _, d := range p.Dirs {
		if _, err := os.Stat(d); err == nil {
			continue
		}
		if err := os.MkdirAll(d, 0o700); err != nil {
			if fail(fmt.Errorf("create %s: %w", d, err)) {
				return errors.Join(errs...)
			}
			continue
		}
		fmt.Fprintf(out, "Created %s\n", d)
	}
	for _, f := range p.Files {
		if err := writeFile(f); err != nil {
			if fail(fmt.Errorf("write %s: %w", f.Path, err)) {
				return errors.Join(errs...)
			}
			continue
		}
		fmt.Fprintf(out, "Wrote %s\n", f.Path)
	}
	run := func(cmds [][]string) bool {
		for _, c := range cmds {
			if len(c) == 0 {
				if fail(errors.New("service: empty command in plan")) {
					return false
				}
				continue
			}
			line := quoteCommand(p.OS, c)
			fmt.Fprintf(out, "Running: %s\n", line)
			o, err := r.Run(ctx, c[0], c[1:]...)
			text := strings.TrimSpace(decodeText([]byte(o)))
			if text != "" {
				fmt.Fprintf(out, "  %s\n", strings.ReplaceAll(text, "\n", "\n  "))
			}
			if err != nil && fail(fmt.Errorf("%s: %w: %s", line, err, text)) {
				return false
			}
		}
		return true
	}
	for _, target := range p.Unload {
		if err := unloadJob(ctx, r, p.OS, target, func(s string) { fmt.Fprintln(out, s) }); err != nil && fail(err) {
			return errors.Join(errs...)
		}
	}
	if !run(p.Commands) {
		return errors.Join(errs...)
	}
	for _, f := range p.RemoveFiles {
		err := os.Remove(f)
		switch {
		case err == nil:
			fmt.Fprintf(out, "Removed %s\n", f)
		case errors.Is(err, os.ErrNotExist):
		default:
			if fail(fmt.Errorf("remove %s: %w", f, err)) {
				return errors.Join(errs...)
			}
		}
	}
	if !run(p.PostCommands) {
		return errors.Join(errs...)
	}
	for _, n := range p.Notes {
		fmt.Fprintf(out, "\n%s\n", n)
	}
	return errors.Join(errs...)
}

// DryRunText is what Apply prints in dry-run mode.
func DryRunText(p Plan) string {
	var b strings.Builder
	b.WriteString("Dry run: nothing is written, removed or run.\n")
	if len(p.Dirs) > 0 {
		b.WriteString("\nWould create, where missing (mode 0700):\n")
		for _, d := range p.Dirs {
			fmt.Fprintf(&b, "  %s\n", d)
		}
	}
	for _, f := range p.Files {
		mode := f.Mode
		if mode == 0 {
			mode = 0o644
		}
		enc := ""
		content := string(f.Content)
		if isUTF16LEBOM(f.Content) {
			enc, content = ", UTF-16LE with BOM, shown decoded", decodeText(f.Content)
		}
		fmt.Fprintf(&b, "\nWould write %s (mode %04o%s):\n", f.Path, mode.Perm(), enc)
		b.WriteString(content)
		if !strings.HasSuffix(content, "\n") {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "(end of %s)\n", f.Path)
	}
	cmds := func(cs [][]string) {
		if len(cs) == 0 {
			return
		}
		b.WriteString("\nWould run:\n")
		for _, c := range cs {
			fmt.Fprintf(&b, "  %s\n", quoteCommand(p.OS, c))
		}
	}
	if len(p.Unload) > 0 {
		b.WriteString("\nWould unload, if loaded (then wait until launchd has removed it):\n")
		for _, t := range p.Unload {
			fmt.Fprintf(&b, "  %s\n", quoteCommand(p.OS, launchctl("bootout", t)))
		}
	}
	cmds(p.Commands)
	if len(p.RemoveFiles) > 0 {
		b.WriteString("\nWould remove:\n")
		for _, f := range p.RemoveFiles {
			fmt.Fprintf(&b, "  %s\n", f)
		}
		if len(p.PostCommands) > 0 {
			b.WriteString("\nThen would run:\n")
			for _, c := range p.PostCommands {
				fmt.Fprintf(&b, "  %s\n", quoteCommand(p.OS, c))
			}
		}
	} else {
		cmds(p.PostCommands)
	}
	for _, n := range p.Notes {
		fmt.Fprintf(&b, "\n%s\n", n)
	}
	return b.String()
}

// writeFile writes f atomically (temporary file in the same directory, then rename), creating
// missing parent directories with mode 0700.
func writeFile(f File) error {
	dir := filepath.Dir(f.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	mode := f.Mode
	if mode == 0 {
		mode = 0o644
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(f.Path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil && runtime.GOOS != "windows" {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(f.Content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.Path)
}

// quoteCommand renders a command as one line for goos: POSIX shell quoting, or Windows
// command-line quoting (what CommandLineToArgvW / the MSVC and Go runtimes parse back).
func quoteCommand(goos string, c []string) string {
	q := make([]string, len(c))
	for i, a := range c {
		if goos == "windows" {
			q[i] = winQuote(a, false)
		} else {
			q[i] = shQuote(a)
		}
	}
	return strings.Join(q, " ")
}

// shQuote quotes s for a POSIX shell: bare when it consists of safe characters only, otherwise in
// single quotes.
func shQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("@%_+=:,./-", r)) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// winQuote quotes one argument for a Windows command line. Backslashes are literal except before a
// double quote, so inside quotes 2n backslashes + quote mean n backslashes and the closing quote,
// and 2n+1 backslashes + quote mean n backslashes and a literal quote. Without force, arguments
// with no blank or quote are left as they are.
func winQuote(s string, force bool) string {
	if s == "" {
		return `""`
	}
	if !force && !strings.ContainsAny(s, " \t\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			slashes++
		case '"':
			b.WriteString(strings.Repeat(`\`, 2*slashes+1))
			b.WriteByte(c)
			slashes = 0
		default:
			b.WriteString(strings.Repeat(`\`, slashes))
			b.WriteByte(c)
			slashes = 0
		}
	}
	b.WriteString(strings.Repeat(`\`, 2*slashes))
	b.WriteByte('"')
	return b.String()
}
