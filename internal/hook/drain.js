// Drains the service-worker notification queue that worker.js fills. Evaluated with returnByValue
// by the Go side; it returns the queued reports as a JSON array of strings (each string is one
// report, exactly as a binding call would carry it) and clears the queue. It never throws.
(function () {
  "use strict";
  try {
    var q = self.__tbdQ;
    if (!Array.isArray(q) || !q.length) return "[]";
    self.__tbdQ = [];
    return JSON.stringify(q);
  } catch (e) {
    return "[]";
  }
})();
