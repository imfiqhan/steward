/* Steward admin glue: CSRF wiring, Tabler re-init across HTMX swaps,
 * toasts, envelope handling, theme toggle. No dependencies beyond
 * tabler.min.js (Bootstrap bundled as window.tabler) and htmx. */
(function () {
  "use strict";

  var csrfToken = function () {
    var meta = document.querySelector('meta[name="csrf-token"]');
    return meta ? meta.getAttribute("content") : "";
  };

  /* ---- Tabler component lifecycle across HTMX swaps -------------------- */

  function tablerInit(root) {
    root = root || document;
    if (!window.tabler) return;
    root.querySelectorAll('[data-bs-toggle="tooltip"]').forEach(function (el) {
      window.tabler.Tooltip.getOrCreateInstance(el, { delay: { show: 50, hide: 50 } });
    });
    root.querySelectorAll('[data-bs-toggle="popover"]').forEach(function (el) {
      window.tabler.Popover.getOrCreateInstance(el);
    });
  }

  function tablerDispose(root) {
    if (!window.tabler) return;
    root.querySelectorAll('[data-bs-toggle="tooltip"],[data-bs-toggle="popover"]').forEach(function (el) {
      var t = window.tabler.Tooltip.getInstance(el);
      if (t) t.dispose();
      var p = window.tabler.Popover.getInstance(el);
      if (p) p.dispose();
    });
  }

  document.addEventListener("DOMContentLoaded", function () {
    tablerInit(document);
  });
  document.body.addEventListener("htmx:afterSettle", function (e) {
    tablerInit(e.target);
    updateSidebarActive();
  });
  document.body.addEventListener("htmx:beforeSwap", function (e) {
    if (e.target && e.target.querySelectorAll) tablerDispose(e.target);
  });
  document.body.addEventListener("htmx:historyRestore", function () {
    tablerInit(document);
    updateSidebarActive();
  });

  /* ---- CSRF on every HTMX request -------------------------------------- */

  document.body.addEventListener("htmx:configRequest", function (e) {
    e.detail.headers["X-CSRF-Token"] = csrfToken();
  });

  /* ---- Sidebar active state under fragment navigation ------------------ */

  function updateSidebarActive() {
    var path = window.location.pathname.replace(/\/$/, "");
    document.querySelectorAll("#sidebar-menu a[href]").forEach(function (a) {
      var href = a.getAttribute("href").replace(/\/$/, "");
      var active = href !== "" && (path === href || path.indexOf(href + "/") === 0);
      a.classList.toggle("active", active);
      var item = a.closest(".nav-item");
      if (item && !item.classList.contains("dropdown")) item.classList.toggle("active", active);
    });
  }

  /* ---- Toasts ----------------------------------------------------------- */

  var TOAST_CLASS = { success: "success", error: "danger", warning: "warning", info: "info" };

  function toast(type, message, detail) {
    var container = document.getElementById("steward-toasts");
    if (!container) { window.alert(message); return; }
    var el = document.createElement("div");
    el.className = "toast align-items-center text-bg-" + (TOAST_CLASS[type] || "info") + " border-0";
    el.setAttribute("role", "alert");
    var body = document.createElement("div");
    body.className = "d-flex";
    var inner = document.createElement("div");
    inner.className = "toast-body";
    inner.textContent = detail ? message + " — " + detail : message;
    var close = document.createElement("button");
    close.type = "button";
    close.className = "btn-close btn-close-white me-2 m-auto";
    close.setAttribute("data-bs-dismiss", "toast");
    close.setAttribute("aria-label", "Close");
    body.appendChild(inner);
    body.appendChild(close);
    el.appendChild(body);
    container.appendChild(el);
    var t = window.tabler ? window.tabler.Toast.getOrCreateInstance(el, { delay: 4000 }) : null;
    if (t) t.show();
    el.addEventListener("hidden.bs.toast", function () { el.remove(); });
  }

  /* ---- Envelope handling (mutations) ------------------------------------ */

  function handleEnvelope(env) {
    if (!env) return;
    if (env.data && env.data.message) {
      toast(env.data.type || (env.status ? "success" : "error"), env.data.message, env.data.detail);
    }
    var then = env.data && env.data.then;
    if (!then) return;
    switch (then.action) {
      case "redirect":
        if (window.htmx) {
          window.htmx.ajax("GET", then.value, { target: "#page-content", swap: "innerHTML show:window:top" });
          window.history.pushState({}, "", then.value);
        } else {
          window.location.assign(then.value);
        }
        break;
      case "location":
        window.location.assign(then.value);
        break;
      case "refresh":
        if (window.htmx) {
          window.htmx.ajax("GET", window.location.pathname + window.location.search, {
            target: "#page-content", swap: "innerHTML"
          });
        } else {
          window.location.reload();
        }
        break;
      case "download":
        window.location.assign(then.value);
        break;
      case "script":
        new Function(then.value)(); // author-supplied, same trust level as the page
        break;
    }
  }

  /* ---- Fetch helper for mutations ---------------------------------------- */

  function request(method, url, body) {
    return fetch(url, {
      method: method,
      headers: {
        "X-CSRF-Token": csrfToken(),
        "Accept": "application/json",
        "X-Requested-With": "XMLHttpRequest"
      },
      body: body || null,
      credentials: "same-origin"
    }).then(function (resp) {
      return resp.json().then(function (env) {
        handleEnvelope(env);
        return env;
      });
    }).catch(function () {
      toast("error", "Request failed — check your connection and retry.");
    });
  }

  /* ---- Grid: row selection + delete actions ------------------------------ */

  function selectedKeys() {
    return Array.prototype.map.call(
      document.querySelectorAll("[data-steward-row-check]:checked"),
      function (el) { return el.value; }
    );
  }

  function refreshBatchUI() {
    var keys = selectedKeys();
    document.querySelectorAll("[data-steward-batch-delete]").forEach(function (btn) {
      btn.classList.toggle("d-none", keys.length === 0);
      var count = btn.querySelector("[data-steward-selected-count]");
      if (count) count.textContent = String(keys.length);
    });
  }

  document.addEventListener("change", function (e) {
    if (e.target.matches("[data-steward-check-all]")) {
      document.querySelectorAll("[data-steward-row-check]").forEach(function (cb) {
        cb.checked = e.target.checked;
      });
      refreshBatchUI();
    } else if (e.target.matches("[data-steward-row-check]")) {
      refreshBatchUI();
    }
  });

  document.addEventListener("click", function (e) {
    var del = e.target.closest("[data-steward-delete]");
    if (del) {
      if (window.confirm("Delete this record? This cannot be undone.")) {
        request("DELETE", del.getAttribute("data-url"));
      }
      return;
    }
    var batch = e.target.closest("[data-steward-batch-delete]");
    if (batch) {
      var keys = selectedKeys();
      if (keys.length === 0) return;
      if (window.confirm("Delete " + keys.length + " selected record(s)? This cannot be undone.")) {
        request("DELETE", batch.getAttribute("data-url") + "/" + keys.join(","));
      }
      return;
    }
    var copy = e.target.closest("[data-steward-copy]");
    if (copy && navigator.clipboard) {
      navigator.clipboard.writeText(copy.getAttribute("data-steward-copy")).then(function () {
        toast("success", "Copied to clipboard.");
      });
    }
  });

  /* ---- Theme toggle ------------------------------------------------------ */

  document.addEventListener("click", function (e) {
    var btn = e.target.closest("[data-steward-theme-toggle]");
    if (!btn) return;
    var html = document.documentElement;
    var next = html.getAttribute("data-bs-theme") === "dark" ? "light" : "dark";
    html.setAttribute("data-bs-theme", next);
    document.cookie = "steward_theme=" + next + "; path=/; max-age=31536000; samesite=lax";
  });

  window.Steward = { toast: toast, handleEnvelope: handleEnvelope, tablerInit: tablerInit, request: request };
})();
