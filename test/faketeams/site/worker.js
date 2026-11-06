// Fake Teams dedicated worker: one fetch, so the tests can check the user agent of worker requests.
"use strict";
try {
  fetch("/api/v1/worker-ping", { cache: "no-store" }).catch(function () {});
} catch (e) {}
