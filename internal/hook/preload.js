// tbd notification hook, injected into every Teams document before the page's own scripts
// (Page.addScriptToEvaluateOnNewDocument). It makes the page believe it is a backgrounded desktop
// app with notification permission, and forwards every notification to the daemon through the CDP
// binding window.__tbd. It must never throw into the page, so everything is wrapped in try/catch.
//
// A tiny preamble sets window.__tbdConfig = {hidden:<bool>} before this runs (the Go side templates
// it). hidden defaults to true when the config is absent. See internal/browser for how it is
// prepended, and docs/DECISIONS.md for why this shape was chosen over a templated preload body.
//
// Re-implemented from the behaviour of Ferdium's notification shim (recipe + app webview); no code
// is copied (licences differ; this project is 0BSD).
(function () {
  "use strict";

  // Names (never values) of the options the page passed, for the daemon's structure-only info log.
  function keysOf(o) {
    try {
      return Object.keys(o || {}).slice(0, 32).map(function (k) { return String(k).slice(0, 40); });
    } catch (e) {
      return [];
    }
  }

  // conversationOf finds the chat a notification belongs to by READING the page (never writing):
  // Teams v2 passes no conversation id with a notification (measured, docs/DECISIONS.md D17), but
  // its chat list renders every chat as an item (role treeitem/option/listitem/row) whose attributes
  // carry the conversation id behind a prefix, with the chat's last-message preview inside. The chat
  // that just received this message shows it as its preview, so: the first item in list order (the
  // chat that just moved to the top) whose preview shows this notification's body. The comparison is
  // anchored — the preview IS the body, "<name>: <body>", or either truncated with an ellipsis — and
  // happens inside the page only. No match means "" (the phone then opens Teams): avatars, titles
  // and labels never pick a chat on their own, because they cannot tell a group message from the
  // sender's 1:1 chat, and channel posts are not in the chat list at all.
  // Returns {id, stats} where stats counts the items seen and the preview matches (diagnostics).
  var CONV_SUB = /\d{1,2}:[A-Za-z0-9._~%+=-]+@(?:thread\.v2|thread\.tacv2|thread\.skype|unq\.gbl\.spaces)|48:notes/;
  var LIST_SEL = '[role="treeitem"],[role="option"],[role="listitem"],[role="row"]';
  // convAttr: the conversation id an element carries, preferring Fluent's tree-item value, then its
  // id, then any attribute.
  function convAttr(el) {
    var at = el.attributes;
    if (!at) return "";
    var pref = ["data-fui-tree-item-value", "id"];
    for (var p = 0; p < pref.length; p++) {
      var pv = el.getAttribute(pref[p]), pm = pv && pv.length < 600 && CONV_SUB.exec(pv);
      if (pm) return pm[0];
    }
    for (var k = 0; k < at.length; k++) {
      var v = at[k].value, m = v && v.length < 600 && CONV_SUB.exec(v);
      if (m) return m[0];
    }
    return "";
  }
  function norm(v) {
    return String(v || "").replace(/[​-‏⁠﻿]/g, "").replace(/\s+/g, " ").trim();
  }
  function untail(v) {
    return v.replace(/(?:…|\.\.\.)$/, "").trim();
  }
  // previewShows: is the text t (one element of an item) this notification's body as a preview?
  function previewShows(t, body) {
    if (!t || !body || t.length > 2000) return false;
    var cands = [t], m = /^[^:]{1,80}:\s(.+)$/.exec(t);
    if (m) cands.push(m[1]);
    var bcut = untail(body);
    for (var i = 0; i < cands.length; i++) {
      var c = cands[i];
      if (c === body) return true;
      var cut = untail(c);
      if (cut !== c && cut.length >= 8 && body.indexOf(cut) === 0) return true; // preview truncated
      if (bcut !== body && bcut.length >= 8 && c.indexOf(bcut) === 0) return true; // body truncated
    }
    return false;
  }
  // labelElements: the elements naming an item (aria-labelledby targets inside it); their text is
  // the chat's title, never its preview.
  function labelElements(item) {
    var out = [];
    try {
      var ids = String(item.getAttribute("aria-labelledby") || "").split(/\s+/);
      for (var i = 0; i < ids.length && i < 8; i++) {
        var l = ids[i] && document.getElementById(ids[i]);
        if (l && item.contains(l)) out.push(l);
      }
    } catch (e) {}
    return out;
  }
  function itemShows(item, body) {
    var skip = labelElements(item);
    var els = item.querySelectorAll("*");
    for (var i = 0; i < els.length && i < 400; i++) {
      var inLabel = false;
      for (var s = 0; s < skip.length && !inLabel; s++) inLabel = skip[s] === els[i] || skip[s].contains(els[i]);
      if (!inLabel && previewShows(norm(els[i].textContent), body)) return true;
    }
    return false;
  }
  function conversationOf(icon, title, body) {
    var stats = { items: 0, matches: 0 }, found = "";
    try {
      var b = norm(body);
      if (!b || typeof document === "undefined" || !document.querySelectorAll) return { id: "", stats: stats };
      var items = document.querySelectorAll(LIST_SEL);
      for (var i = 0; i < items.length && i < 3000; i++) {
        var id = convAttr(items[i]);
        if (!id) continue;
        stats.items++;
        if (itemShows(items[i], b)) {
          stats.matches++;
          if (!found) found = id;
        }
      }
    } catch (e) {}
    return { id: found, stats: stats };
  }

  try {
    var g = typeof window !== "undefined" ? window : self;
    var MARK = Symbol.for("tbd.hook.installed");
    if (g[MARK]) return; // idempotent: runs in every frame and on every new document
    try {
      Object.defineProperty(g, MARK, { value: true, enumerable: false, configurable: true });
    } catch (e) {
      g[MARK] = true;
    }

    var cfg = {};
    try {
      cfg = g.__tbdConfig || {};
      // Read once, then remove it, so the page does not see a stray global.
      delete g.__tbdConfig;
    } catch (e) {}
    var HIDDEN = cfg.hidden !== false;

    // binding() looks up the CDP binding lazily, so a notification fired before the binding is
    // installed still reports once it is. If the binding is missing, the hook does nothing.
    function binding() {
      try {
        if (typeof g.__tbd === "function") return g.__tbd;
      } catch (e) {}
      try {
        if (typeof __tbd === "function") return __tbd;
      } catch (e) {}
      return null;
    }
    // encode serialises a report within the daemon's 64 KiB payload cap (measured in UTF-8 bytes,
    // as the daemon measures it). options.data is page-controlled and may be cyclic, hold a BigInt
    // or be huge: rather than lose the whole notification, drop data and flag it (dataError), and as
    // a last resort cut the text fields to the daemon's caps.
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
    // later sends a notification report once the page shows which chat it belongs to: the chat
    // list catches up shortly after Teams raises the toast, so try at +100 ms and, failing that, at
    // +600 ms (outside Teams' toast pipeline either way). A report still pending when the document
    // unloads is resolved and sent right away, so a reload in that window loses nothing.
    var pending = [];
    function finish(p, final) {
      if (p.sent) return;
      var r = { id: "", stats: { items: 0, matches: 0 } };
      try {
        r = conversationOf(p.obj.icon, p.obj.title, p.obj.body);
      } catch (e) {}
      if (!r.id && !final) return;
      p.sent = true;
      p.obj.conversation = r.id;
      p.obj.resolve = "items:" + r.stats.items + ",matches:" + r.stats.matches;
      for (var i = 0; i < pending.length; i++) if (pending[i] === p) pending.splice(i, 1);
      send(p.obj);
    }
    function flush() {
      var all = pending.slice();
      for (var i = 0; i < all.length; i++) finish(all[i], true);
    }
    try {
      g.addEventListener("pagehide", flush, true);
      g.addEventListener("beforeunload", flush, true);
    } catch (e) {}
    function later(obj) {
      var p = { obj: obj, sent: false };
      pending.push(p);
      try {
        setTimeout(function () { finish(p, false); }, 100);
        setTimeout(function () { finish(p, true); }, 600);
      } catch (e) {
        finish(p, true);
      }
    }
    function send(obj) {
      var fn = binding();
      if (!fn) return;
      var s = encode(obj);
      if (s === null) return;
      try {
        fn(s);
      } catch (e) {}
    }
    function href() {
      try {
        return String(location.href);
      } catch (e) {
        return "";
      }
    }
    function str(v) {
      return v == null ? "" : String(v);
    }

    // ---- window.Notification shim --------------------------------------------------------------
    try {
      var Native = g.Notification;

      function WrapNotification(title, options) {
        options = options || {};
        this.title = str(title);
        this.body = options.body != null ? String(options.body) : "";
        this.tag = options.tag != null ? String(options.tag) : "";
        this.data = options.data !== undefined ? options.data : null;
        this.icon = options.icon != null ? String(options.icon) : "";
        this.badge = options.badge != null ? String(options.badge) : "";
        this.image = options.image != null ? String(options.image) : "";
        this.dir = options.dir || "auto";
        this.lang = options.lang || "";
        this.renotify = !!options.renotify;
        this.requireInteraction = !!options.requireInteraction;
        this.silent = !!options.silent;
        this.timestamp = options.timestamp != null ? options.timestamp : Date.now();
        this.onclick = null;
        this.onclose = null;
        this.onerror = null;
        this.onshow = null;
        this._listeners = Object.create(null);

        later({
          kind: "notification",
          source: "window",
          title: this.title,
          body: this.body,
          tag: this.tag,
          data: this.data,
          icon: this.icon,
          timestamp: typeof options.timestamp === "number" ? options.timestamp : null,
          href: href(),
          optionKeys: keysOf(options),
        });

        // Fire "show" asynchronously, like the real API.
        var self = this;
        try {
          setTimeout(function () {
            self._dispatch("show");
          }, 0);
        } catch (e) {}
      }

      WrapNotification.permission = "granted";
      WrapNotification.maxActions = (Native && Native.maxActions) || 2;
      WrapNotification.requestPermission = function (cb) {
        // Resolve "granted" and also call a legacy callback argument if given.
        if (typeof cb === "function") {
          try {
            cb("granted");
          } catch (e) {}
        }
        return Promise.resolve("granted");
      };

      WrapNotification.prototype.addEventListener = function (type, cb) {
        if (typeof cb !== "function") return;
        (this._listeners[type] || (this._listeners[type] = [])).push(cb);
      };
      WrapNotification.prototype.removeEventListener = function (type, cb) {
        var a = this._listeners[type];
        if (!a) return;
        for (var i = a.length - 1; i >= 0; i--) if (a[i] === cb) a.splice(i, 1);
      };
      WrapNotification.prototype.dispatchEvent = function (ev) {
        this._dispatch(ev && ev.type, ev);
        return true;
      };
      WrapNotification.prototype._dispatch = function (type, ev) {
        if (!type) return;
        ev = ev || { type: type };
        try {
          ev.target = ev.target || this;
          ev.currentTarget = this;
        } catch (e) {}
        var on = this["on" + type];
        if (typeof on === "function") {
          try {
            on.call(this, ev);
          } catch (e) {}
        }
        var a = this._listeners[type];
        if (a) {
          for (var i = 0; i < a.length; i++) {
            try {
              a[i].call(this, ev);
            } catch (e) {}
          }
        }
      };
      WrapNotification.prototype.close = function () {
        this._dispatch("close");
      };

      // Note: the prototype is deliberately NOT linked to the native Notification.prototype. The
      // native prototype exposes title/body/tag/… as getter-only accessors, so a chained prototype
      // would make the constructor's `this.title = …` assignments throw in strict mode. The cost is
      // that `x instanceof Notification` is false for a shim instance, which Teams does not rely on.
      try {
        g.Notification = WrapNotification;
      } catch (e) {
        try {
          Object.defineProperty(g, "Notification", { value: WrapNotification, configurable: true, writable: true });
        } catch (e2) {}
      }
    } catch (e) {}

    // ---- ServiceWorkerRegistration.prototype.showNotification (called from the page) -----------
    try {
      var SWR = g.ServiceWorkerRegistration;
      if (SWR && SWR.prototype && !SWR.prototype[MARK]) {
        var origShow = SWR.prototype.showNotification;
        SWR.prototype.showNotification = function (title, options) {
          options = options || {};
          later({
            kind: "notification",
            source: "sw-page",
            title: str(title),
            body: options.body != null ? String(options.body) : "",
            tag: options.tag != null ? String(options.tag) : "",
            data: options.data !== undefined ? options.data : null,
            icon: options.icon != null ? String(options.icon) : "",
            timestamp: typeof options.timestamp === "number" ? options.timestamp : null,
            href: href(),
            optionKeys: keysOf(options),
          });
          // Still call the original so the page sees normal behaviour.
          if (typeof origShow === "function") {
            try {
              return origShow.apply(this, arguments);
            } catch (e) {
              return Promise.reject(e); // as the native promise-returning API would
            }
          }
          return Promise.resolve();
        };
        try {
          Object.defineProperty(SWR.prototype, MARK, { value: true, enumerable: false, configurable: true });
        } catch (e) {}
      }
    } catch (e) {}

    // ---- Pretend the document is hidden and unfocused, so Teams raises notifications -----------
    if (HIDDEN && typeof document !== "undefined") {
      try {
        document.hasFocus = function () {
          return false;
        };
      } catch (e) {}
      var hiddenGetters = [
        ["hidden", true],
        ["visibilityState", "hidden"],
        ["webkitHidden", true],
        ["webkitVisibilityState", "hidden"],
      ];
      for (var h = 0; h < hiddenGetters.length; h++) {
        (function (name, val) {
          try {
            Object.defineProperty(document, name, {
              configurable: true,
              get: function () {
                return val;
              },
            });
          } catch (e) {}
        })(hiddenGetters[h][0], hiddenGetters[h][1]);
      }
    }

    // ---- Observe document.title for a leading "(N)" unread count (log-only liveness) -----------
    // Top-level document only: iframes have titles of their own that would make the count flap.
    var isTop = false;
    try {
      isTop = g.top === g;
    } catch (e) {}
    if (isTop && typeof document !== "undefined") {
      var lastCount = -1;
      var checkTitle = function () {
        try {
          var m = /^\s*\((\d{1,6})\+?\)/.exec(document.title || "");
          var n = m ? parseInt(m[1], 10) : 0;
          if (isNaN(n)) n = 0;
          if (n !== lastCount) {
            lastCount = n;
            send({ kind: "title", source: "window", count: n, href: href() });
          }
        } catch (e) {}
      };
      checkTitle();
      try {
        var mo = new MutationObserver(checkTitle);
        // Observe the whole <head> (not just the current <title>), so a replaced <title> element is
        // seen too, and check once on attach for a title set before the observer existed.
        var attach = function () {
          try {
            var root = document.head || document.documentElement;
            if (root) mo.observe(root, { subtree: true, childList: true, characterData: true });
          } catch (e) {}
          checkTitle();
        };
        if (document.head) attach();
        else document.addEventListener("DOMContentLoaded", attach, { once: true });
      } catch (e) {}
    }

    // ---- One "hooked" ping per document --------------------------------------------------------
    send({ kind: "hooked", source: "window", href: href() });
  } catch (e) {
    // Never throw into the page.
  }
})();
