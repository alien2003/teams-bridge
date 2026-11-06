package hook

import _ "embed"

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
