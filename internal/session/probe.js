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
    // Lower case, diacritics removed (NFD, then the combining marks: "é" → "e", "ё" → "е"),
    // curly apostrophes folded, whitespace collapsed. The lists below go through the same.
    const norm = (s) =>
      String(s == null ? "" : s)
        .toLowerCase()
        .normalize("NFD")
        .replace(/[\u0300-\u036f]/g, "")
        .replace(/[‘’ʼ`]/g, "'")
        .replace(/\s+/g, " ")
        .trim();
    const label = (s) => norm(s).replace(/[\s.!:>…›→]+$/, "");

    // Whole labels of a sign-in button or link: English, German, French, Spanish, Portuguese,
    // Italian, Dutch, Polish, Ukrainian, Russian.
    const LABELS = new Set(
      [
        "sign in",
        "sign in again",
        "sign back in",
        "sign in to teams",
        "sign in to microsoft teams",
        "sign in with another account",
        "use another account",
        "use a different account",
        "anmelden",
        "erneut anmelden",
        "wieder anmelden",
        "bei teams anmelden",
        "anderes konto verwenden",
        "mit einem anderen konto anmelden",
        "se connecter",
        "se reconnecter",
        "utiliser un autre compte",
        "se connecter avec un autre compte",
        "iniciar sesión",
        "volver a iniciar sesión",
        "iniciar sesión de nuevo",
        "usar otra cuenta",
        "iniciar sesión con otra cuenta",
        "entrar",
        "entrar novamente",
        "iniciar sessão",
        "usar outra conta",
        "utilizar outra conta",
        "entrar com outra conta",
        "accedi",
        "accedi di nuovo",
        "accedi nuovamente",
        "usa un altro account",
        "accedi con un altro account",
        "aanmelden",
        "opnieuw aanmelden",
        "een ander account gebruiken",
        "aanmelden met een ander account",
        "zaloguj się",
        "zaloguj się ponownie",
        "zaloguj ponownie",
        "użyj innego konta",
        "zaloguj się przy użyciu innego konta",
        "увійти",
        "увійти знову",
        "увійти ще раз",
        "увійти повторно",
        "використати інший обліковий запис",
        "використовувати інший обліковий запис",
        "войти",
        "войти снова",
        "войти еще раз",
        "войти повторно",
        "выполнить вход",
        "войти с другой учетной записью",
        "использовать другую учетную запись",
      ].map(label),
    );
    // Phrases of a signed-out notice, matched anywhere in the text; the same languages.
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
      "sie wurden abgemeldet",
      "sie sind abgemeldet",
      "sitzung ist abgelaufen",
      "melden sie sich erneut an",
      "konto auswählen",
      "vous êtes déconnecté",
      "vous avez été déconnecté",
      "session a expiré",
      "reconnectez-vous",
      "connectez-vous pour continuer",
      "choisir un compte",
      "sélectionner un compte",
      "se cerró la sesión",
      "ha cerrado la sesión",
      "sesión ha expirado",
      "sesión ha caducado",
      "vuelva a iniciar sesión",
      "inicie sesión para continuar",
      "elegir una cuenta",
      "seleccione una cuenta",
      "sessão expirou",
      "entre novamente",
      "inicie sessão novamente",
      "escolha uma conta",
      "escolher uma conta",
      "sei stato disconnesso",
      "sessione è scaduta",
      "accedi di nuovo",
      "accedi per continuare",
      "scegli un account",
      "u bent afgemeld",
      "je bent afgemeld",
      "sessie is verlopen",
      "meld u opnieuw aan",
      "meld je opnieuw aan",
      "kies een account",
      "zostałeś wylogowany",
      "nastąpiło wylogowanie",
      "sesja wygasła",
      "zaloguj się ponownie",
      "zaloguj się, aby kontynuować",
      "wybierz konto",
      "ви вийшли з",
      "сеанс завершився",
      "сеанс закінчився",
      "термін дії сеансу минув",
      "увійдіть знову",
      "увійдіть ще раз",
      "увійдіть повторно",
      "виберіть обліковий запис",
      "вы вышли из",
      "сеанс истек",
      "срок действия сеанса истек",
      "войдите снова",
      "войдите еще раз",
      "войдите повторно",
      "выполните вход",
      "выберите учетную запись",
    ].map(norm);
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
