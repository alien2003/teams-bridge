// Fake Teams service worker. On activation it claims the page, so it controls it. When the page
// posts a "notify" message it raises a notification from inside the worker, which exercises the
// worker-side hook (self.registration.showNotification → source sw-worker), and it makes a fetch of
// its own, so the tests can check the user agent of service-worker requests.
//
// Determinism: the daemon hooks a worker moments after it starts, and the real Teams worker is
// long-lived, so its notifications arrive after the hook is in place. To model that without a
// race, this worker waits (up to 10 s) for the hook's install mark before it shows the
// notification, and then shows it exactly once, hooked or not (so a missing hook still fails the
// tests instead of hanging them).
"use strict";

var HOOK_MARK = Symbol.for("tbd.hook.sw.installed");

self.addEventListener("install", function (event) {
  self.skipWaiting();
});

self.addEventListener("activate", function (event) {
  event.waitUntil(self.clients.claim());
});

function whenHooked(timeoutMs) {
  return new Promise(function (resolve) {
    var start = Date.now();
    (function check() {
      if (self[HOOK_MARK] === true || Date.now() - start > timeoutMs) return resolve();
      setTimeout(check, 50);
    })();
  });
}

self.addEventListener("message", function (event) {
  var data = event.data || {};
  if (data.type !== "notify") return;
  event.waitUntil(
    whenHooked(10000).then(function () {
      var title = data.title || "";
      try {
        self.registration.showNotification(title, data.options || {}).catch(function () {});
      } catch (e) {}
      try {
        fetch("/report", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ type: "raised", source: "sw-worker", title: title }),
        }).catch(function () {});
        return fetch("/api/v1/sw-ping", { cache: "no-store" }).catch(function () {});
      } catch (e) {}
    })
  );
});
