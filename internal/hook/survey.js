// Structure-only survey of the Teams page's chat list, evaluated by the daemon once per browser
// session (Runtime.evaluate, returnByValue). It answers "would the conversation resolver work on
// this page?" without any notification: how many elements hold a whole conversation id, under which
// attribute names, which id kinds, whether they carry an accessible label and avatars with a
// participant id. Only counts, names and masked shapes (letters → a, digits → 9) are returned.
(function () {
  "use strict";
  var CONV = /^(?:\d{1,2}:[A-Za-z0-9._~%+=-]+@(?:thread\.v2|thread\.tacv2|thread\.skype|unq\.gbl\.spaces)|48:notes)$/;
  function shape(v, max) {
    var out = "";
    for (var i = 0; i < v.length && i < max; i++) {
      var c = v.charAt(i);
      out += /[0-9]/.test(c) ? "9" : /[A-Za-z]/.test(c) || c.charCodeAt(0) > 127 ? "a" : c;
    }
    return v.length > max ? out + "…" : out;
  }
  function labelled(el) {
    try {
      var ids = String(el.getAttribute("aria-labelledby") || "").split(/\s+/);
      for (var i = 0; i < ids.length && i < 8; i++) {
        var l = ids[i] && document.getElementById(ids[i]);
        if (l && String(l.textContent || "").trim().length >= 2) return true;
      }
      return String(el.getAttribute("aria-label") || "").trim().length >= 2;
    } catch (e) {
      return false;
    }
  }
  var out = { elements: 0, scanned: 0, items: 0, attrs: {}, kinds: {}, labelled: 0, avatarMRI: 0, avatarImg: 0, samples: [], ms: 0, path: "" };
  try {
    var t0 = Date.now();
    var els = document.querySelectorAll("*");
    out.elements = els.length;
    out.scanned = Math.min(els.length, 30000);
    out.path = shape(String(location.pathname || ""), 40);
    var seen = {};
    for (var i = 0; i < out.scanned; i++) {
      var at = els[i].attributes;
      if (!at) continue;
      for (var j = 0; j < at.length; j++) {
        var v = at[j].value;
        if (!v || v.length > 300 || !CONV.test(v) || seen[v]) continue;
        seen[v] = true;
        out.items++;
        out.attrs[at[j].name] = (out.attrs[at[j].name] || 0) + 1;
        var kind = v.indexOf("@") >= 0 ? v.slice(v.indexOf("@") + 1) : v;
        out.kinds[kind] = (out.kinds[kind] || 0) + 1;
        if (labelled(els[i])) out.labelled++;
        if (els[i].querySelector("[data-person-mri]")) out.avatarMRI++;
        if (els[i].querySelector('img[src*="/users/"]')) out.avatarImg++;
        if (out.samples.length < 3) out.samples.push(shape(v, 80));
        break;
      }
    }
    out.ms = Date.now() - t0;
  } catch (e) {
    out.error = true;
  }
  return out;
})();
