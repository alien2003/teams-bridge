// tbd session probe (internal/session, BRIEF §5 detector 3). An expression for Runtime.evaluate
// with returnByValue: it reads the page, changes nothing, and evaluates to
// {href, signin, why}. See ProbeJS in probe.go for the heuristics.
(() => {
  "use strict";
  const out = { href: "", signin: false, why: "" };
  try {
    out.href = String(location.href);
  } catch (e) {
    return out;
  }
  try {
    const norm = (s) =>
      String(s == null ? "" : s)
        .toLowerCase()
        .replace(/[‘’ʼ`]/g, "'")
        .replace(/ё/g, "е")
        .replace(/\s+/g, " ")
        .trim();
    const label = (s) => norm(s).replace(/[\s.!:>…›→]+$/, "");

    const LABELS = new Set([
      "sign in",
      "sign in again",
      "sign back in",
      "sign in to teams",
      "sign in to microsoft teams",
      "sign in with another account",
      "use another account",
      "use a different account",
      "войти",
      "войти снова",
      "войти еще раз",
      "войти повторно",
      "выполнить вход",
      "войти с другой учетной записью",
      "использовать другую учетную запись",
    ]);
    const PHRASES = [
      "you're signed out",
      "you are signed out",
      "you've been signed out",
      "you have been signed out",
      "your session has expired",
      "your session expired",
      "session has timed out",
      "sign in again",
      "sign in to continue",
      "please sign in",
      "pick an account",
      "вы вышли из",
      "сеанс истек",
      "срок действия сеанса истек",
      "войдите снова",
      "войдите еще раз",
      "войдите повторно",
      "выполните вход",
      "выберите учетную запись",
    ];
    const LOGIN_HOSTS = ["login.microsoftonline.com", "login.live.com", "login.microsoft.com", "login.windows.net"];
    const CLICKABLE = 'button,a[href],[role="button"],[role="link"],input[type="submit"],input[type="button"]';
    // Everything a user can operate: the Teams app shell has far more than 20 of these (app bar,
    // chat list items, search, composer), a login or landing page far fewer.
    const INTERACTIVE =
      CLICKABLE +
      ',input:not([type="hidden"]),select,textarea,[role="tab"],[role="menuitem"],[role="treeitem"],' +
      '[role="option"],[role="checkbox"],[role="switch"],[role="textbox"],[role="searchbox"],[contenteditable="true"],[contenteditable=""]';
    const MODAL = 'dialog[open],[role="dialog"],[role="alertdialog"],[aria-modal="true"]';
    const TOAST = '[role="alert"],[role="status"]';

    const vw = window.innerWidth || document.documentElement.clientWidth || 0;
    const vh = window.innerHeight || document.documentElement.clientHeight || 0;
    // rendered: painted and on screen. visible: rendered and not inside an aria-hidden/inert
    // subtree (what a user can see and operate).
    const rendered = (el, minW, minH) => {
      if (!el || !el.isConnected) return false;
      if (typeof el.checkVisibility === "function") {
        if (!el.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true })) return false;
      } else {
        for (let n = el; n && n.nodeType === 1; n = n.parentElement) {
          const cs = getComputedStyle(n);
          if (cs.display === "none" || cs.visibility === "hidden" || cs.opacity === "0") return false;
        }
      }
      const r = el.getBoundingClientRect();
      if (r.width < (minW || 2) || r.height < (minH || 2)) return false;
      return r.right > 0 && r.bottom > 0 && (!vw || r.left < vw) && (!vh || r.top < vh);
    };
    const visible = (el, minW, minH) => rendered(el, minW, minH) && !el.closest('[aria-hidden="true"],[inert]');
    const all = (sel) => Array.prototype.slice.call(document.querySelectorAll(sel));
    const anyVisible = (sel) => all(sel).some((el) => visible(el));
    const textOf = (el) => {
      let t = el instanceof HTMLInputElement ? el.value : el.innerText || el.textContent || "";
      if (!norm(t)) t = el.getAttribute("aria-label") || el.getAttribute("title") || "";
      return label(t);
    };
    const isLoginHost = (src) => {
      let h = "";
      try {
        h = new URL(src, location.href).hostname.toLowerCase();
      } catch (e) {
        return false;
      }
      return LOGIN_HOSTS.some((b) => h === b || h.endsWith("." + b));
    };
    const hit = (why) => {
      out.signin = true;
      out.why = why;
      return out;
    };

    if (anyVisible('input[name="loginfmt"],input[name="passwd"],#i0116,#i0118')) return hit("login-form");
    if (anyVisible("#tilesHolder")) return hit("account-picker");
    if (all("iframe[src]").some((f) => isLoginHost(f.getAttribute("src")) && visible(f, 200, 200))) {
      return hit("login-iframe");
    }

    const clickables = all(CLICKABLE).filter((el) => visible(el));
    // Sparse counts rendered elements, aria-hidden or not: a modal dialog makes Fluent UI hide
    // the app shell behind it from assistive technology, yet the shell (and the message text
    // in its chat list) is still on screen, so the page is not a login or landing page.
    let shown = 0;
    for (const el of all(INTERACTIVE)) {
      if (rendered(el) && ++shown > 20) break;
    }
    const sparse = shown <= 20;
    if (clickables.some((el) => (sparse || el.closest(MODAL + "," + TOAST)) && LABELS.has(textOf(el)))) {
      return hit("signin-button");
    }
    if (all('input[type="email"],input[type="password"]').some((el) => (sparse || el.closest(MODAL)) && visible(el))) {
      return hit("credential-input");
    }
    const scopes = sparse && document.body ? [document.body] : all(MODAL).filter((el) => visible(el));
    for (const s of scopes) {
      const t = norm(String(s.innerText || "").slice(0, 8000));
      if (PHRASES.some((p) => t.includes(p))) return hit("signed-out-text");
    }
  } catch (e) {
    out.signin = false;
    out.why = "probe-exception";
  }
  return out;
})()
