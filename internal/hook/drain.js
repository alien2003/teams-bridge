// Drains the service-worker notification queue that worker.js fills. Evaluated with returnByValue
// by the Go side; it returns the queued reports as a JSON array of strings (each string is one
// report, exactly as a binding call would carry it) and removes them from the queue, at most 64 per
// call: worker code can replace the array, so the size worker.js enforces cannot be relied on.
// It never throws.
(function () {
  "use strict";
  try {
    var q = self.__tbdQ;
    if (!Array.isArray(q) || !q.length) return "[]";
    var out = q.splice(0, 64).filter(function (x) { return typeof x === "string"; });
    return JSON.stringify(out);
  } catch (e) {
    return "[]";
  }
})();
