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
    document.querySelectorAll("[data-steward-batch-delete], [data-steward-action][data-batch]").forEach(function (btn) {
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
      return;
    }
    var action = e.target.closest("[data-steward-action]");
    if (action) {
      var ids = [];
      if (action.hasAttribute("data-batch")) {
        ids = selectedKeys();
        if (ids.length === 0) return;
      } else if (action.getAttribute("data-ids")) {
        ids = [action.getAttribute("data-ids")];
      }
      var confirmMsg = action.getAttribute("data-confirm");
      if (confirmMsg && !window.confirm(confirmMsg)) return;
      var body = new URLSearchParams();
      if (ids.length > 0) body.set("ids", ids.join(","));
      action.disabled = true;
      fetch(action.getAttribute("data-url"), {
        method: "POST",
        headers: {
          "X-CSRF-Token": csrfToken(),
          "Accept": "application/json",
          "X-Requested-With": "XMLHttpRequest",
          "Content-Type": "application/x-www-form-urlencoded"
        },
        body: body.toString(),
        credentials: "same-origin"
      }).then(function (resp) { return resp.json(); }).then(handleEnvelope).catch(function () {
        toast("error", "Action failed — check your connection and retry.");
      }).finally(function () { action.disabled = false; });
    }
  });

  /* ---- Forms: envelope submit + 422 field errors + uploads --------------- */

  function clearFieldErrors(form) {
    form.querySelectorAll("[data-steward-field]").forEach(function (wrap) {
      wrap.querySelectorAll(".is-invalid").forEach(function (el) { el.classList.remove("is-invalid"); });
      var box = wrap.querySelector("[data-steward-errors]");
      if (box) box.textContent = "";
    });
  }

  function showFieldErrors(form, errors) {
    Object.keys(errors || {}).forEach(function (name) {
      var wrap = form.querySelector('[data-steward-field="' + CSS.escape(name) + '"]');
      if (!wrap) return;
      var input = wrap.querySelector(".form-control, .form-select, .form-check-input");
      if (input) input.classList.add("is-invalid");
      var box = wrap.querySelector("[data-steward-errors]");
      if (box) box.textContent = errors[name].join(" ");
    });
    var first = form.querySelector(".is-invalid");
    if (first) first.focus();
  }

  document.addEventListener("submit", function (e) {
    var form = e.target.closest("[data-steward-form]");
    if (!form) return;
    e.preventDefault();
    clearFieldErrors(form);
    var btn = form.querySelector("[data-steward-submit]");
    if (btn) { btn.disabled = true; btn.classList.add("btn-loading"); }
    fetch(form.getAttribute("action"), {
      method: form.getAttribute("data-method") || "POST",
      headers: {
        "X-CSRF-Token": csrfToken(),
        "Accept": "application/json",
        "X-Requested-With": "XMLHttpRequest"
      },
      body: new FormData(form),
      credentials: "same-origin"
    }).then(function (resp) { return resp.json(); }).then(function (env) {
      if (env && env.errors) {
        showFieldErrors(form, env.errors);
        toast("error", "Please fix the highlighted fields.");
        return;
      }
      handleEnvelope(env);
    }).catch(function () {
      toast("error", "Request failed — check your connection and retry.");
    }).finally(function () {
      if (btn) { btn.disabled = false; btn.classList.remove("btn-loading"); }
    });
  });

  document.addEventListener("change", function (e) {
    var input = e.target.closest("[data-steward-upload]");
    if (!input || !input.files || input.files.length === 0) return;
    var wrap = input.closest("[data-steward-field]");
    var hidden = wrap.querySelector("[data-steward-upload-value]");
    var preview = wrap.querySelector("[data-steward-upload-preview]");
    var fd = new FormData();
    fd.append("file", input.files[0]);
    input.disabled = true;
    fetch(input.getAttribute("data-steward-upload"), {
      method: "POST",
      headers: { "X-CSRF-Token": csrfToken(), "Accept": "application/json" },
      body: fd,
      credentials: "same-origin"
    }).then(function (resp) { return resp.json(); }).then(function (out) {
      if (!out || out.status !== true) {
        toast("error", (out && out.data && out.data.message) || "Upload failed.");
        return;
      }
      if (hidden) hidden.value = out.path;
      if (preview) {
        preview.classList.remove("d-none");
        var img = preview.querySelector("img");
        if (img) { img.src = out.url; }
        else { preview.innerHTML = '<a href="' + out.url + '" target="_blank" rel="noopener">Uploaded file</a>'; }
      }
      toast("success", "Uploaded.");
    }).catch(function () {
      toast("error", "Upload failed — check your connection and retry.");
    }).finally(function () { input.disabled = false; });
  });

  /* ---- Inline grid editing ----------------------------------------------- */

  function putField(url, field, params) {
    var body = new URLSearchParams(params);
    body.set("_inline", "1");
    return fetch(url, {
      method: "PUT",
      headers: {
        "X-CSRF-Token": csrfToken(),
        "Accept": "application/json",
        "X-Requested-With": "XMLHttpRequest",
        "Content-Type": "application/x-www-form-urlencoded"
      },
      body: body.toString(),
      credentials: "same-origin"
    }).then(function (resp) { return resp.json(); });
  }

  document.addEventListener("change", function (e) {
    var sw = e.target.closest("[data-steward-inline-switch]");
    if (!sw) return;
    var field = sw.getAttribute("data-field");
    var params = {};
    params["_present_" + field] = "1";
    if (sw.checked) params[field] = "on";
    sw.disabled = true;
    putField(sw.getAttribute("data-url"), field, params).then(function (env) {
      if (env && env.errors) {
        sw.checked = !sw.checked;
        toast("error", Object.values(env.errors).flat().join(" "));
        return;
      }
      handleEnvelope(env);
    }).catch(function () {
      sw.checked = !sw.checked;
      toast("error", "Save failed — check your connection and retry.");
    }).finally(function () { sw.disabled = false; });
  });

  function startInlineEdit(cell) {
    if (cell.querySelector("input")) return;
    var original = cell.textContent;
    var input = document.createElement("input");
    input.type = "text";
    input.className = "form-control form-control-sm d-inline-block";
    input.style.width = Math.max(120, cell.offsetWidth + 24) + "px";
    input.value = original;
    cell.textContent = "";
    cell.appendChild(input);
    input.focus();
    input.select();

    var done = false;
    function finish(save) {
      if (done) return;
      done = true;
      var next = input.value;
      cell.textContent = original;
      if (!save || next === original) return;
      var field = cell.getAttribute("data-field");
      var params = {};
      params[field] = next;
      putField(cell.getAttribute("data-url"), field, params).then(function (env) {
        if (env && env.errors) {
          toast("error", Object.values(env.errors).flat().join(" "));
          return;
        }
        cell.textContent = next;
        handleEnvelope(env);
      }).catch(function () {
        toast("error", "Save failed — check your connection and retry.");
      });
    }
    input.addEventListener("keydown", function (ev) {
      if (ev.key === "Enter") { ev.preventDefault(); finish(true); }
      if (ev.key === "Escape") finish(false);
    });
    input.addEventListener("blur", function () { finish(true); });
  }

  document.addEventListener("click", function (e) {
    var cell = e.target.closest("[data-steward-editable]");
    if (cell) startInlineEdit(cell);
  });
  document.addEventListener("keydown", function (e) {
    if (e.key !== "Enter") return;
    var cell = e.target.closest && e.target.closest("[data-steward-editable]");
    if (cell && !cell.querySelector("input")) { e.preventDefault(); startInlineEdit(cell); }
  });

  /* ---- Theme toggle ------------------------------------------------------ */

  document.addEventListener("click", function (e) {
    var btn = e.target.closest("[data-steward-theme-toggle]");
    if (!btn) return;
    e.preventDefault();
    var html = document.documentElement;
    var next = html.getAttribute("data-bs-theme") === "dark" ? "light" : "dark";
    html.setAttribute("data-bs-theme", next);
    document.cookie = "steward_theme=" + next + "; path=/; max-age=31536000; samesite=lax";
  });

  window.Steward = { toast: toast, handleEnvelope: handleEnvelope, tablerInit: tablerInit, request: request };
})();
