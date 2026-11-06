// tbd worker-side hook, evaluated inside a Teams service-worker target (Runtime.evaluate). It wraps
// showNotification so a notification raised by the worker itself is captured, then calls the
// original so the worker behaves normally. It must never throw. Idempotent.
//
// Delivery: each captured notification is serialised at capture time (so a later mutation of
// options.data cannot change it, and a non-serialisable data cannot lose it) and sent through the
// CDP binding self.__tbd, which the Go side installs on the worker target (Runtime.addBinding)
// before evaluating this. If the binding is missing or the call fails, the JSON string goes onto
// self.__tbdQ instead, a bounded queue that the Go side drains with a periodic Runtime.evaluate
// (see WorkerDrainJS). Each report takes exactly one of the two paths.
//
// It wraps ServiceWorkerRegistration.prototype.showNotification (which self.registration inherits),
// and, only if that is not possible, the live self.registration instance.
//
// Re-implemented from the behaviour of Ferdium's notification shim; no code is copied.
(function () {
  "use strict";
  try {
    var g = self;
    var MARK = Symbol.for("tbd.hook.sw.installed");
    if (g[MARK]) return; // idempotent

    function href() {
      try {
        if (g.registration && g.registration.scope) return String(g.registration.scope);
      } catch (e) {}
      try {
        return String(g.location && g.location.href);
      } catch (e) {}
      return "";
    }
    function str(v) {
      return v == null ? "" : String(v);
    }
    // encode mirrors preload.js: stay within the daemon's 64 KiB cap (UTF-8 bytes); drop data that
    // does not serialise or is too large (dataError) rather than losing the notification.
    var MAX = 63 * 1024;
    function blen(s) {
      try {
        return new TextEncoder().encode(s).length;
      } catch (e) {
        return s.length * 3;
      }
    }
    function cut(v, n) {
      return typeof v === "string" && v.length > n ? v.slice(0, n) : v;
    }
    function encode(obj) {
      var s = null;
      try {
        s = JSON.stringify(obj);
      } catch (e) {}
      if (typeof s === "string" && blen(s) <= MAX) return s;
      try {
        obj.data = null;
        obj.dataError = true;
        s = JSON.stringify(obj);
        if (blen(s) > MAX) {
          obj.title = cut(obj.title, 512);
          obj.body = cut(obj.body, 8192);
          obj.tag = cut(obj.tag, 512);
          obj.href = cut(obj.href, 4096);
          if (typeof obj.icon === "string" && obj.icon.length > 4096) obj.icon = "";
          s = JSON.stringify(obj);
        }
        return s;
      } catch (e) {
        return null;
      }
    }
    function push(obj) {
      try {
        var s = encode(obj);
        if (s === null) return;
        var fn = null;
        try {
          if (typeof g.__tbd === "function") fn = g.__tbd;
        } catch (e) {}
        if (fn) {
          try {
            fn(s);
            return;
          } catch (e) {}
        }
        if (!Array.isArray(g.__tbdQ)) g.__tbdQ = [];
        g.__tbdQ.push(s);
        // Cap the queue so a misbehaving worker cannot grow it without bound.
        if (g.__tbdQ.length > 256) g.__tbdQ.splice(0, g.__tbdQ.length - 256);
      } catch (e) {}
    }
    function report(title, options) {
      options = options || {};
      push({
        kind: "notification",
        source: "sw-worker",
        title: str(title),
        body: options.body != null ? String(options.body) : "",
        tag: options.tag != null ? String(options.tag) : "",
        data: options.data !== undefined ? options.data : null,
        icon: options.icon != null ? String(options.icon) : "",
        href: href(),
      });
    }

    // wrapShow replaces target.showNotification with a wrapper that captures first, then calls the
    // original. It defines an own property, so it works on an instance or a prototype.
    var HOOKED = Symbol.for("tbd.hook.sw.wrapped");
    function wrapShow(target) {
      if (!target || typeof target.showNotification !== "function" || target[HOOKED]) return false;
      var orig = target.showNotification;
      var wrapper = function (title, options) {
        report(title, options);
        try {
          return orig.apply(this, arguments);
        } catch (e) {
          return Promise.reject(e);
        }
      };
      try {
        Object.defineProperty(target, "showNotification", {
          value: wrapper,
          writable: true,
          enumerable: false,
          configurable: true,
        });
        Object.defineProperty(target, HOOKED, { value: true, enumerable: false, configurable: true });
        return true;
      } catch (e) {
        return false;
      }
    }

    var ok = false;
    try {
      if (g.ServiceWorkerRegistration && g.ServiceWorkerRegistration.prototype) {
        ok = wrapShow(g.ServiceWorkerRegistration.prototype);
      }
    } catch (e) {}
    if (!ok) {
      try {
        ok = wrapShow(g.registration);
      } catch (e) {}
    }

    // Mark only once something is wrapped, so a failed attempt can be retried.
    try {
      Object.defineProperty(g, MARK, { value: ok, enumerable: false, configurable: true });
    } catch (e) {}
    if (ok) push({ kind: "hooked", source: "sw-worker", href: href() });
    // The evaluate's result tells the Go side whether the hook is in place.
    return ok;
  } catch (e) {
    // Never throw.
    return false;
  }
})();
