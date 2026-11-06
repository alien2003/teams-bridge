// Fake Teams page behaviour for the tbd E2E tests. It exercises every path the hook must cover:
// a window.Notification, a ServiceWorkerRegistration.showNotification from the page, a
// self.registration.showNotification from inside the worker, the unread-count title, the
// high-entropy client hints and the page's visibility/focus/permission state, a dedicated worker
// and a service worker that fetch on their own (user agent of worker requests), plus a periodic API
// poll (for the 401-burst detector) and a command poll (login redirect, on-demand notifications).
// Every notification it raises is also reported to the server (type "raised"), exactly once.
(function () {
  "use strict";

  var status = function (t) {
    try {
      document.getElementById("status").textContent = t;
    } catch (e) {}
  };

  function report(obj) {
    try {
      fetch("/report", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(obj),
        keepalive: true,
      }).catch(function () {});
    } catch (e) {}
  }
  function raised(source, title) {
    report({ type: "raised", source: source, title: title });
  }
  function notify(title, options) {
    try {
      new Notification(title, options);
      raised("window", title);
    } catch (e) {}
  }

  // 1. Report the environment the hook produced: client hints, visibility, focus, permission.
  function reportEnv() {
    var base = {
      type: "env",
      ua: navigator.userAgent,
      hidden: document.hidden,
      visibilityState: document.visibilityState,
      hasFocus: document.hasFocus(),
      permission: (window.Notification && window.Notification.permission) || "default",
      brands: (navigator.userAgentData && navigator.userAgentData.brands) || null,
      mobile: navigator.userAgentData ? navigator.userAgentData.mobile : null,
      platform: navigator.userAgentData ? navigator.userAgentData.platform : null,
    };
    if (navigator.userAgentData && navigator.userAgentData.getHighEntropyValues) {
      navigator.userAgentData
        .getHighEntropyValues(["platformVersion", "architecture", "bitness", "fullVersionList", "model", "uaFullVersion", "wow64"])
        .then(function (hev) {
          base.highEntropy = hev;
          report(base);
        })
        .catch(function () {
          report(base);
        });
    } else {
      report(base);
    }
  }
  reportEnv();

  // 2. A window.Notification (source: window), shaped as Teams v2 raises it (measured 2026-11-06):
  //    no data, an empty tag, the sender's id only in the avatar URL (work-tenant layout). The hook resolves the chat
  //    from the chat list above (Alice's 1:1 chat, not the group she is also in).
  notify("Alice", {
    body: "hi",
    icon: location.origin + "/api/mt/part/zz-01/beta/users/00000000-1111-4222-8333-000000000000/profilepicturev2/8:orgid:00000000-0000-4000-8000-00000000a11c?displayname=Alice",
    timestamp: Date.now(),
    tag: "",
    requireInteraction: false,
    dir: "auto",
  });

  // 3. Unread-count title.
  try {
    document.title = "(3) Chat | Microsoft Teams";
  } catch (e) {}

  // 4. A dedicated worker that makes one fetch of its own (user agent of worker requests).
  try {
    new Worker("worker.js");
  } catch (e) {}

  // 5. Service worker: register, show from the page, then ask the worker (once) to show from
  //    itself. The worker waits until it is hooked before it shows (see sw.js).
  if ("serviceWorker" in navigator) {
    navigator.serviceWorker
      .register("sw.js", { scope: "/v2/" })
      .then(function () {
        return navigator.serviceWorker.ready;
      })
      .then(function (reg) {
        status("service worker ready");
        try {
          reg.showNotification("Bob", {
            body: "from sw-page",
            tag: "19:bob@unq.gbl.spaces",
            data: { chatId: "19:bob@unq.gbl.spaces", messageId: "1648741500777" },
          });
          raised("sw-page", "Bob");
        } catch (e) {}
        if (reg.active) {
          reg.active.postMessage({
            type: "notify",
            title: "Carol",
            options: {
              body: "from sw-worker",
              tag: "19:carol@thread.tacv2",
              data: { channelId: "19:carol@thread.tacv2", messageId: "1648741500888" },
            },
          });
        }
      })
      .catch(function (e) {
        status("service worker failed: " + e);
      });
  }

  // 6. Periodic API poll, so the 401-burst detector has traffic (resource type Fetch).
  setInterval(function () {
    fetch("/api/v1/poll", { method: "GET", cache: "no-store" }).catch(function () {});
  }, 500);

  // 7. Command poll: the server can move the main frame to the fake login host, or queue extra
  //    window notifications (Server.RedirectToLogin, Server.Notify).
  setInterval(function () {
    fetch("/v2/nav-check", { method: "GET", cache: "no-store" })
      .then(function (r) {
        return r.json();
      })
      .then(function (j) {
        if (!j) return;
        if (j.notify && j.notify.length) {
          for (var i = 0; i < j.notify.length; i++) {
            var n = j.notify[i] || {};
            notify(String(n.title || ""), n.options || {});
          }
        }
        if (j.redirect) {
          location.href = j.redirect;
        }
      })
      .catch(function () {});
  }, 500);
})();
