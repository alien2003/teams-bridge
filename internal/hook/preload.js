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

  // Structure-only search of the page for the sender's id (taken from the avatar URL): which
  // elements carry it, and does an ancestor carry an attribute that looks like a conversation id?
  // Only tag/attribute names and masked shapes (letters → a, digits → 9) leave this function.
  // Diagnostics for the deep-link design; never throws.
  function shapeOf(v, max) {
    v = String(v);
    var out = "";
    for (var i = 0; i < v.length && i < max; i++) {
      var c = v.charAt(i);
      if (/[0-9]/.test(c)) out += "9";
      else if (/\s/.test(c)) out += " ";
      else if (/[A-Za-z]/.test(c) || c.charCodeAt(0) > 127) out += "a";
      else out += c;
    }
    return v.length > max ? out + "…" : out;
  }
  function senderOf(icon) {
    try {
      var m = /\/users\/([^\/?#]+)/.exec(String(icon || ""));
      if (!m) return "";
      var id = decodeURIComponent(m[1]);
      return /^\d{1,2}:[A-Za-z0-9._:@-]{3,200}$/.test(id) ? id : "";
    } catch (e) {
      return "";
    }
  }
  var CONV_RE = /\d{1,2}:[A-Za-z0-9._%:-]{3,}@[A-Za-z0-9.]+/;
  function locate(icon) {
    var out = [];
    try {
      var id = senderOf(icon);
      if (!id || typeof document === "undefined" || !document.querySelectorAll) return out;
      var enc = encodeURIComponent(id);
      var els = document.querySelectorAll("*");
      var n = Math.min(els.length, 30000), hits = 0, t0 = Date.now();
      for (var i = 0; i < n; i++) {
        var attrs = els[i].attributes;
        if (!attrs) continue;
        for (var j = 0; j < attrs.length; j++) {
          var v = attrs[j].value;
          if (!v || (v.indexOf(id) < 0 && v.indexOf(enc) < 0)) continue;
          hits++;
          if (out.length < 12) {
            var found = "", a = els[i], lvl = 0;
            while (a && a.attributes && lvl < 15 && !found) {
              for (var k = 0; k < a.attributes.length; k++) {
                var av = a.attributes[k].value, mm = av && CONV_RE.exec(av);
                if (mm && mm[0].indexOf(id) < 0) {
                  found = " conv@" + lvl + ":" + a.tagName.toLowerCase() + "[" + a.attributes[k].name + "]=" + shapeOf(mm[0], 80);
                  break;
                }
              }
              a = a.parentElement;
              lvl++;
            }
            out.push(els[i].tagName.toLowerCase() + "[" + attrs[j].name + "]" + (found || " conv=none"));
          }
          break;
        }
      }
      out.unshift("hits=" + hits + " scanned=" + n + " ms=" + (Date.now() - t0));
    } catch (e) {
      out.push("error");
    }
    return out;
  }

  // conversationOf finds the conversation a notification belongs to by reading the page (never
  // writing): Teams v2 passes no conversation id with the notification, but the sender's id is in the
  // avatar URL, and the chat list renders each chat as an element whose attribute holds the
  // conversation id ("19:…@thread.v2", measured: Fluent's data-fui-tree-item-value) around avatars
  // that carry the participant's id (data-person-mri, img src). One pass over the DOM collects
  // (a) elements carrying the sender id, resolved to their nearest ancestor holding a conversation
  // id (+1 each), and (b) every element whose whole attribute value is a conversation id, scored by
  // its accessible name (aria-labelledby) against the notification title: equal +3, one containing
  // the other +2 — that separates a 1:1 chat from a group with the same person, and finds groups
  // whose item shows no avatar. Highest score wins; ties go to the first in the list (most recent).
  // Returns "" if nothing usable is found.
  var CONV_FULL = /^(?:\d{1,2}:[A-Za-z0-9._~%+=-]+@(?:thread\.v2|thread\.tacv2|thread\.skype|unq\.gbl\.spaces)|48:notes)$/;
  function convAttr(el) {
    var at = el.attributes;
    if (!at) return "";
    for (var k = 0; k < at.length; k++) {
      var v = at[k].value;
      if (v && v.length < 300 && CONV_FULL.test(v)) return v;
    }
    return "";
  }
  function convNear(el) {
    for (var lvl = 0; el && lvl < 15; lvl++, el = el.parentElement) {
      var c = convAttr(el);
      if (c) return c;
    }
    return "";
  }
  // labelOf is an element's accessible name from aria-labelledby (or aria-label), normalised.
  function labelOf(el) {
    try {
      var ids = String(el.getAttribute("aria-labelledby") || "").split(/\s+/), parts = [];
      for (var i = 0; i < ids.length && i < 8; i++) {
        var l = ids[i] && document.getElementById(ids[i]);
        if (l) parts.push(l.textContent || "");
      }
      var text = parts.length ? parts.join(" ") : String(el.getAttribute("aria-label") || "");
      return text.replace(/\s+/g, " ").trim().slice(0, 300);
    } catch (e) {
      return "";
    }
  }
  function conversationOf(icon, title) {
    try {
      if (typeof document === "undefined" || !document.querySelectorAll) return "";
      var id = senderOf(icon), enc = id ? encodeURIComponent(id) : "";
      var t = String(title || "").replace(/\s+/g, " ").trim();
      var score = {}, order = [], labelled = {};
      function add(c, s) {
        if (!c) return;
        if (!(c in score)) {
          score[c] = 0;
          order.push(c);
        }
        score[c] += s;
      }
      var els = document.querySelectorAll("*");
      var n = Math.min(els.length, 30000);
      for (var i = 0; i < n; i++) {
        var el = els[i], at = el.attributes;
        if (!at) continue;
        var item = convAttr(el);
        if (item && t && !labelled[item]) {
          labelled[item] = true;
          var label = labelOf(el);
          if (label.length >= 2) {
            if (label === t) add(item, 3);
            else if (t.indexOf(label) >= 0 || label.indexOf(t) >= 0) add(item, 2);
          }
        }
        if (!id) continue;
        for (var j = 0; j < at.length; j++) {
          var v = at[j].value;
          if (v && (v.indexOf(id) >= 0 || v.indexOf(enc) >= 0)) {
            add(convNear(el), 1);
            break;
          }
        }
      }
      var best = "", bestScore = 0;
      for (var m = 0; m < order.length; m++) {
        if (score[order[m]] > bestScore) {
          best = order[m];
          bestScore = score[order[m]];
        }
      }
      return best;
    } catch (e) {
      return "";
    }
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

        send({
          kind: "notification",
          source: "window",
          title: this.title,
          body: this.body,
          tag: this.tag,
          data: this.data,
          icon: this.icon,
          href: href(),
          optionKeys: keysOf(options),
          domProbe: locate(this.icon),
          conversation: conversationOf(this.icon, this.title),
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
          send({
            kind: "notification",
            source: "sw-page",
            title: str(title),
            body: options.body != null ? String(options.body) : "",
            tag: options.tag != null ? String(options.tag) : "",
            data: options.data !== undefined ? options.data : null,
            icon: options.icon != null ? String(options.icon) : "",
            href: href(),
            optionKeys: keysOf(options),
            conversation: conversationOf(options.icon, title),
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
