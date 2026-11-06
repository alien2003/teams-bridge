package hook

import (
	_ "embed"
	"fmt"
	"sort"
	"strings"
)

// PreloadJS is the hook injected into every Teams document before its own scripts run
// (Page.addScriptToEvaluateOnNewDocument). The Go side prepends a one-line preamble that sets
// window.__tbdConfig = {hidden: <bool>} (the preload reads and deletes it; absent means hidden);
// see internal/browser.
//
//go:embed preload.js
var PreloadJS string

// WorkerJS is the worker-side hook, evaluated inside a Teams service-worker target
// (Runtime.evaluate) after Runtime.addBinding(BindingName) on that target. It wraps
// showNotification and reports each captured notification through the binding, or, when the
// binding is missing, queues the same JSON string on self.__tbdQ for WorkerDrainJS. The
// evaluation's value is true when the hook is in place.
//
//go:embed worker.js
var WorkerJS string

// WorkerDrainJS reads and clears the service-worker queue that WorkerJS fills. The Go side
// evaluates it periodically with returnByValue; it returns a JSON array of strings, one untrusted
// hook payload (for Parse) each.
//
//go:embed drain.js
var WorkerDrainJS string

// SurveyJS is a structure-only survey of the page's chat list (counts, attribute names, id kinds,
// masked samples), evaluated once per browser session to show whether the conversation resolver
// can work on this tenant's page; see survey.js and docs/DECISIONS.md D17.
//
//go:embed survey.js
var SurveyJS string

// Survey is SurveyJS's result.
type Survey struct {
	Elements  int            `json:"elements"`
	Scanned   int            `json:"scanned"`
	Items     int            `json:"items"`
	Attrs     map[string]int `json:"attrs"`
	Kinds     map[string]int `json:"kinds"`
	Labelled  int            `json:"labelled"`
	AvatarMRI int            `json:"avatarMRI"`
	AvatarImg int            `json:"avatarImg"`
	Samples   []string       `json:"samples"`
	MS        int            `json:"ms"`
	Path      string         `json:"path"`
	Error     bool           `json:"error"`
}

// Counts renders a name→count map for a log line ("a:3,b:1"), capped, names masked if odd.
func Counts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for i, k := range keys {
		if i == 10 {
			parts = append(parts, fmt.Sprintf("…(+%d)", len(keys)-10))
			break
		}
		name := k
		if len(name) > 40 || strings.ContainsAny(name, " :@/") {
			name = Shape(name, 40)
		}
		parts = append(parts, fmt.Sprintf("%s:%d", name, m[k]))
	}
	return strings.Join(parts, ",")
}
