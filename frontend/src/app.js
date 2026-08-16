/* Steward UI bundle: htmx + Basecoat components + admin glue.
 * Built by tools/assets (esbuild, pure Go) into assets/dist/app.js —
 * no Node at runtime. Basecoat's core watches the DOM with a
 * MutationObserver, so htmx swaps initialize themselves. */
import htmx from "../vendor/htmx/htmx.esm.js";
import "../vendor/basecoat/js/all.js";
// Basecoat ships its Chart component outside the full bundle, and it attaches
// with `window.basecoat && (window.basecoat.chart = ...)` — it only takes if
// basecoat already exists. Importing it here puts it after all.js and inside
// the one deferred script, which is the only ordering that holds: a tag in the
// page body runs before this bundle does, finds no basecoat, and silently
// attaches nothing. Chart.js itself stays out — it is larger than this whole
// bundle and most pages have no chart.
import "../vendor/basecoat/js/chart.min.js";

window.htmx = htmx;

(function () {
  "use strict";

  var csrfToken = function () {
    var meta = document.querySelector('meta[name="csrf-token"]');
    return meta ? meta.getAttribute("content") : "";
  };

  /* ---- CSRF on every HTMX request ---------------------------------------- */

  document.addEventListener("htmx:configRequest", function (e) {
    e.detail.headers["X-CSRF-Token"] = csrfToken();
  });

  /* ---- Toasts (Basecoat toaster: method call on #toaster) ----------------- */

  function toast(type, message, detail) {
    var category = { success: "success", error: "error", warning: "warning", info: "info" }[type] || "info";
    var toaster = document.getElementById("toaster");
    if (toaster && typeof toaster.toast === "function") {
      toaster.toast({ category: category, title: message, description: detail || "" });
      return;
    }
    // Toaster not initialized yet (e.g. very early error): degrade politely.
    console[category === "error" ? "error" : "log"]("[steward]", message, detail || "");
  }

  /* ---- Basecoat × htmx history restore -------------------------------------
     htmx serializes initialized DOM into its history cache; strip the
     runtime flags before caching and force re-init after restore. Normal
     swaps are covered by Basecoat's own MutationObserver. */

  function clearSerializedRuntimeState(root) {
    var all = root.querySelectorAll ? root.querySelectorAll("*") : [];
    for (var i = 0; i < all.length; i++) {
      var el = all[i];
      delete el.dataset.basecoatComponent;
      Array.prototype.slice.call(el.attributes).forEach(function (attr) {
        if (/^data-[a-z0-9-]+-initialized$/.test(attr.name)) {
          el.removeAttribute(attr.name);
        }
      });
    }
  }

  document.addEventListener("htmx:historyItemCreated", function (event) {
    var item = event.detail && event.detail.item;
    if (!item || typeof item.content !== "string") return;
    var template = document.createElement("template");
    template.innerHTML = item.content;
    clearSerializedRuntimeState(template.content);
    item.content = template.innerHTML;
  });

  document.addEventListener("htmx:historyRestore", function () {
    if (window.basecoat && window.basecoat.initAll) {
      window.basecoat.initAll({ force: true });
    }
  });

  /* ---- Envelope handling (mutations) -------------------------------------- */

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

  /* ---- Fetch helper for mutations ------------------------------------------ */

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

  /* ---- Generic show/hide toggles (filter panel) ----------------------------- */

  document.addEventListener("click", function (e) {
    var t = e.target.closest("[data-steward-toggle]");
    if (!t) return;
    var target = document.querySelector(t.getAttribute("data-steward-toggle"));
    if (!target) return;
    var hidden = target.classList.toggle("hidden");
    t.setAttribute("aria-expanded", hidden ? "false" : "true");
  });

  /* ---- Sidebar active state under fragment navigation ---------------------- */

  function updateSidebarActive() {
    var path = window.location.pathname.replace(/\/$/, "");
    document.querySelectorAll("#sidebar-menu a[href]").forEach(function (a) {
      var href = a.getAttribute("href").replace(/\/$/, "");
      var active = href !== "" && (path === href || path.indexOf(href + "/") === 0);
      if (active) {
        a.setAttribute("aria-current", "page");
      } else {
        a.removeAttribute("aria-current");
      }
    });
  }
  document.addEventListener("htmx:afterSettle", updateSidebarActive);
  document.addEventListener("DOMContentLoaded", updateSidebarActive);

  /* ---- Grid: row selection + delete + copy + custom actions ---------------- */

  function selectedKeys() {
    return Array.prototype.map.call(
      document.querySelectorAll("[data-steward-row-check]:checked"),
      function (el) { return el.value; }
    );
  }

  function refreshBatchUI() {
    var keys = selectedKeys();
    document.querySelectorAll("[data-steward-batch-delete], [data-steward-action][data-batch]").forEach(function (btn) {
      btn.classList.toggle("hidden", keys.length === 0);
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

  // Basecoat JS selects (div.select) update their hidden input and fire a
  // bubbling change event, but unlike native selects never submit a form;
  // data-steward-submit opts a select into submitting the form it sits in.
  document.addEventListener("change", function (e) {
    if (!(e.target instanceof Element)) return;
    var sel = e.target.closest("div.select[data-steward-submit]");
    if (!sel) return;
    var form = sel.closest("form");
    if (form) form.requestSubmit();
  });

  /* ---- Confirmation modal (alert-dialog in layout/base.html) --------------- */

  // Replaces window.confirm with the Basecoat alert-dialog; resolves to
  // true only when the primary action is chosen (Esc/Cancel → false).
  function confirmDialog(opts) {
    var dlg = document.getElementById("steward-confirm");
    if (!dlg || typeof dlg.showModal !== "function") {
      var msg = opts.description ? opts.title + " " + opts.description : opts.title;
      return Promise.resolve(window.confirm(msg));
    }
    document.getElementById("steward-confirm-title").textContent = opts.title || "Are you sure?";
    var desc = document.getElementById("steward-confirm-desc");
    desc.textContent = opts.description || "";
    desc.classList.toggle("hidden", !opts.description);
    var ok = dlg.querySelector("[data-steward-confirm-ok]");
    ok.textContent = opts.action || "Confirm";
    if (opts.danger) ok.setAttribute("data-variant", "destructive");
    else ok.removeAttribute("data-variant");
    return new Promise(function (resolve) {
      var confirmed = false;
      function onOk() { confirmed = true; dlg.close(); }
      ok.addEventListener("click", onOk);
      dlg.addEventListener("close", function () {
        ok.removeEventListener("click", onOk);
        resolve(confirmed);
      }, { once: true });
      dlg.showModal();
    });
  }

  document.addEventListener("click", function (e) {
    var del = e.target.closest("[data-steward-delete]");
    if (del) {
      confirmDialog({
        title: "Delete this record?",
        description: "This cannot be undone.",
        action: "Delete",
        danger: true
      }).then(function (yes) {
        if (yes) request("DELETE", del.getAttribute("data-url"));
      });
      return;
    }
    var batch = e.target.closest("[data-steward-batch-delete]");
    if (batch) {
      var keys = selectedKeys();
      if (keys.length === 0) return;
      confirmDialog({
        title: "Delete " + keys.length + " selected record(s)?",
        description: "This cannot be undone.",
        action: "Delete",
        danger: true
      }).then(function (yes) {
        if (yes) request("DELETE", batch.getAttribute("data-url") + "/" + keys.join(","));
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
      var proceed = confirmMsg
        ? confirmDialog({ title: confirmMsg, danger: action.getAttribute("data-variant") === "destructive" })
        : Promise.resolve(true);
      proceed.then(function (yes) {
        if (!yes) return;
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
      });
    }
  });

  /* ---- Column show/hide (persisted in localStorage per grid) --------------- */

  function hiddenCols(slug) {
    try { return JSON.parse(localStorage.getItem("steward-cols-" + slug)) || []; }
    catch (err) { return []; }
  }

  function applyColumnPrefs() {
    document.querySelectorAll("[data-steward-grid]").forEach(function (table) {
      var slug = table.getAttribute("data-steward-grid");
      var hidden = hiddenCols(slug);
      table.querySelectorAll("[data-col]").forEach(function (el) {
        el.classList.toggle("hidden", hidden.indexOf(el.getAttribute("data-col")) >= 0);
      });
      var pick = document.querySelector('[data-steward-colpick="' + CSS.escape(slug) + '"]');
      if (pick) {
        pick.querySelectorAll("input[type=checkbox]").forEach(function (cb) {
          cb.checked = hidden.indexOf(cb.value) < 0;
        });
      }
    });
    // Hiding or showing a column changes whether the table overflows, which is
    // what decides the pinned column's divider. Hoisted, so defined below.
    initHScroll();
  }

  document.addEventListener("change", function (e) {
    var pick = e.target.closest("[data-steward-colpick]");
    if (!pick || !e.target.matches("input[type=checkbox]")) return;
    var slug = pick.getAttribute("data-steward-colpick");
    var hidden = hiddenCols(slug).filter(function (v) { return v !== e.target.value; });
    if (!e.target.checked) hidden.push(e.target.value);
    localStorage.setItem("steward-cols-" + slug, JSON.stringify(hidden));
    applyColumnPrefs();
  });

  document.addEventListener("DOMContentLoaded", applyColumnPrefs);
  document.addEventListener("htmx:afterSettle", applyColumnPrefs);

  /* ---- Nested (hasMany) form rows ------------------------------------------ */

  var nestedCounter = 0;

  document.addEventListener("click", function (e) {
    var add = e.target.closest("[data-steward-nested-add]");
    if (add) {
      var wrap = add.closest("[data-steward-nested]");
      var tpl = wrap.querySelector("[data-steward-nested-template]");
      var rows = wrap.querySelector("[data-steward-nested-rows]");
      var key = "new_" + (++nestedCounter) + "_" + Math.random().toString(36).slice(2, 7);
      var html = tpl.innerHTML.split("__KEY__").join(key);
      var holder = document.createElement("div");
      holder.innerHTML = html;
      while (holder.firstElementChild) rows.appendChild(holder.firstElementChild);
      return;
    }
    var rm = e.target.closest("[data-steward-nested-remove]");
    if (rm) {
      var row = rm.closest("[data-steward-nested-row]");
      var rowKey = row.getAttribute("data-key");
      var nested = row.closest("[data-steward-nested]");
      if (rowKey.indexOf("new_") === 0) {
        row.remove();
        return;
      }
      var input = document.createElement("input");
      input.type = "hidden";
      input.name = nested.getAttribute("data-steward-nested") + "[" + rowKey + "][_remove]";
      input.value = "1";
      row.appendChild(input);
      row.classList.add("hidden");
      return;
    }
  });

  /* ---- Tree grid collapse --------------------------------------------------- */

  document.addEventListener("click", function (e) {
    var btn = e.target.closest("[data-steward-tree-toggle]");
    if (!btn) return;
    var row = btn.closest("tr");
    var depth = parseInt(row.getAttribute("data-depth") || "0", 10);
    var expanded = btn.getAttribute("aria-expanded") !== "false";
    btn.setAttribute("aria-expanded", expanded ? "false" : "true");
    btn.textContent = expanded ? "▸" : "▾";
    var sib = row.nextElementSibling;
    while (sib && parseInt(sib.getAttribute("data-depth") || "0", 10) > depth) {
      if (expanded) {
        sib.classList.add("hidden");
      } else {
        sib.classList.remove("hidden");
        var t = sib.querySelector("[data-steward-tree-toggle]");
        if (t && t.getAttribute("aria-expanded") === "false") {
          var d = parseInt(sib.getAttribute("data-depth") || "0", 10);
          var inner = sib.nextElementSibling;
          while (inner && parseInt(inner.getAttribute("data-depth") || "0", 10) > d) {
            inner.classList.add("hidden");
            inner = inner.nextElementSibling;
          }
          sib = inner;
          continue;
        }
      }
      sib = sib.nextElementSibling;
    }
  });

  /* ---- Row reordering (HTML5 drag and drop) --------------------------------- */

  var dragRow = null;

  document.addEventListener("dragstart", function (e) {
    var row = e.target.closest && e.target.closest("tbody[data-steward-reorder] > tr[draggable]");
    if (!row) return;
    dragRow = row;
    row.classList.add("steward-dragging");
    e.dataTransfer.effectAllowed = "move";
    e.dataTransfer.setData("text/plain", row.getAttribute("data-key") || "");
  });

  document.addEventListener("dragover", function (e) {
    if (!dragRow) return;
    var over = e.target.closest && e.target.closest("tbody[data-steward-reorder] > tr[draggable]");
    if (!over || over === dragRow || over.parentNode !== dragRow.parentNode) return;
    e.preventDefault();
    var rect = over.getBoundingClientRect();
    var after = e.clientY > rect.top + rect.height / 2;
    over.parentNode.insertBefore(dragRow, after ? over.nextSibling : over);
  });

  document.addEventListener("dragend", function () {
    if (!dragRow) return;
    var tbody = dragRow.closest("tbody[data-steward-reorder]");
    dragRow.classList.remove("steward-dragging");
    dragRow = null;
    if (!tbody) return;
    var keys = Array.prototype.map.call(
      tbody.querySelectorAll("tr[data-key]"),
      function (tr) { return tr.getAttribute("data-key"); }
    ).filter(Boolean);
    if (keys.length === 0) return;
    var body = new URLSearchParams();
    body.set("ids", keys.join(","));
    fetch(tbody.getAttribute("data-steward-reorder"), {
      method: "POST",
      headers: {
        "X-CSRF-Token": csrfToken(),
        "Accept": "application/json",
        "Content-Type": "application/x-www-form-urlencoded"
      },
      body: body.toString(),
      credentials: "same-origin"
    }).then(function (resp) { return resp.json(); }).then(handleEnvelope).catch(function () {
      toast("error", "Reorder failed — reload and retry.");
    });
  });

  /* ---- Inline grid editing --------------------------------------------------- */

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
    input.className = "input";
    input.style.width = Math.max(140, cell.offsetWidth + 24) + "px";
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

  /* ---- Forms: envelope submit + 422 field errors + uploads ------------------ */

  function clearFieldErrors(form) {
    form.querySelectorAll("[data-steward-field]").forEach(function (wrap) {
      wrap.querySelectorAll("[aria-invalid]").forEach(function (el) { el.removeAttribute("aria-invalid"); });
      var box = wrap.querySelector("[data-steward-errors]");
      if (box) box.textContent = "";
    });
  }

  function showFieldErrors(form, errors) {
    Object.keys(errors || {}).forEach(function (name) {
      var wrap = form.querySelector('[data-steward-field="' + CSS.escape(name) + '"]');
      if (!wrap) return;
      var input = wrap.querySelector("input, select, textarea");
      if (input) input.setAttribute("aria-invalid", "true");
      var box = wrap.querySelector("[data-steward-errors]");
      if (box) box.textContent = errors[name].join(" ");
    });
    var first = form.querySelector("[aria-invalid]");
    if (first) first.focus();
  }

  /* ---- Confirm before submitting a form ------------------------------------ */
  /*
   * Opt a plain form into the alert dialog with data-steward-confirm-submit. The
   * grid's actions already confirm, but they post through fetch; a native form —
   * turning off two-factor authentication, say — had no way to ask first.
   *
   * Registered before the resource-form handler below so it gates that too: the
   * first listener wins the preventDefault, and re-submits once confirmed.
   */

  document.addEventListener("submit", function (e) {
    var form = e.target.closest("[data-steward-confirm-submit]");
    if (!form || form.dataset.stewardConfirmed === "1") return;
    e.preventDefault();
    e.stopImmediatePropagation();
    confirmDialog({
      title: form.dataset.confirmTitle || "Are you sure?",
      description: form.dataset.confirmDescription || "",
      action: form.dataset.confirmAction || "Confirm",
      danger: form.dataset.confirmDanger === "1"
    }).then(function (yes) {
      if (!yes) return;
      form.dataset.stewardConfirmed = "1";
      // requestSubmit rather than submit, so validation and the other submit
      // handlers still run; the flag above stops this one looping.
      if (typeof form.requestSubmit === "function") form.requestSubmit();
      else form.submit();
      delete form.dataset.stewardConfirmed;
    });
  }, true);

  document.addEventListener("submit", function (e) {
    var form = e.target.closest("[data-steward-form]");
    if (!form) return;
    e.preventDefault();
    clearFieldErrors(form);
    var btn = form.querySelector("[data-steward-submit]");
    if (btn) btn.disabled = true;
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
      if (btn) btn.disabled = false;
    });
  });

  /* ---- File and image uploads ---------------------------------------------- */
  /*
   * The native input draws its own control and cannot show a value it did not
   * receive, so an edit form said "no file selected" over a record that had one.
   * It stays as the file source — clicks and drops are forwarded to it — and
   * everything visible is built around it.
   *
   * XHR rather than fetch: fetch reports no upload progress, and a large file on
   * a slow line otherwise leaves the field looking dead.
   */

  function uploadParts(root) {
    return {
      input: root.querySelector("[data-steward-upload-input]"),
      hidden: root.querySelector("[data-steward-upload-value]"),
      pick: root.querySelector("[data-steward-upload-pick]"),
      current: root.querySelector("[data-steward-upload-current]"),
      thumb: root.querySelector("[data-steward-upload-thumb]"),
      fileIcon: root.querySelector("[data-steward-upload-icon]"),
      link: root.querySelector("[data-steward-upload-link]"),
      note: root.querySelector("[data-steward-upload-note]"),
      progress: root.querySelector("[data-steward-upload-progress]"),
      bar: root.querySelector("[data-steward-upload-bar]"),
    };
  }

  // showUpload switches between the two states the field has: nothing held, or
  // one file held and named.
  function showUpload(root, file) {
    var p = uploadParts(root);
    var isImage = root.dataset.kind === "image";
    if (!file || !file.path) {
      if (p.hidden) p.hidden.value = "";
      if (p.pick) p.pick.hidden = false;
      if (p.current) p.current.hidden = true;
      return;
    }
    if (p.hidden) p.hidden.value = file.path;
    if (p.pick) p.pick.hidden = true;
    if (p.current) p.current.hidden = false;
    if (p.link) {
      p.link.textContent = file.name || file.path;
      p.link.href = file.url || "";
    }
    if (p.note) { p.note.hidden = true; p.note.textContent = ""; }
    if (isImage && p.thumb) {
      p.thumb.hidden = false;
      if (p.fileIcon) p.fileIcon.hidden = true;
      p.thumb.src = file.url || "";
    }
  }

  // A stored file can be missing — the row outlived it, or the media was never
  // carried across. A broken-image glyph says nothing, so the field says it.
  function markUploadMissing(root) {
    var p = uploadParts(root);
    if (p.thumb) p.thumb.hidden = true;
    if (p.fileIcon) p.fileIcon.hidden = false;
    if (p.note) {
      p.note.textContent = "This file is missing from storage.";
      p.note.hidden = false;
    }
  }

  function uploadProgress(root, ratio) {
    var p = uploadParts(root);
    if (!p.progress || !p.bar) return;
    if (ratio === null) {
      p.progress.hidden = true;
      return;
    }
    p.progress.hidden = false;
    p.bar.style.width = Math.round(ratio * 100) + "%";
  }

  function sendUpload(root, file, done) {
    var p = uploadParts(root);
    if (!file || !p.hidden) { if (done) done(false); return; }
    root.dataset.busy = "1";
    uploadProgress(root, 0);

    var fd = new FormData();
    fd.append("file", file);
    var xhr = new XMLHttpRequest();
    xhr.open("POST", root.dataset.stewardUpload, true);
    xhr.setRequestHeader("X-CSRF-Token", csrfToken());
    xhr.setRequestHeader("Accept", "application/json");
    xhr.withCredentials = true;
    xhr.upload.addEventListener("progress", function (ev) {
      if (ev.lengthComputable) uploadProgress(root, ev.loaded / ev.total);
    });
    xhr.addEventListener("load", function () {
      delete root.dataset.busy;
      uploadProgress(root, null);
      var out = null;
      try { out = JSON.parse(xhr.responseText); } catch (err) { /* reported below */ }
      if (!out || out.status !== true) {
        toast("error", (out && out.data && out.data.message) || "Upload failed.");
        if (done) done(false);
        return;
      }
      if (done) { done(true, out); return; }
      showUpload(root, { path: out.path, url: out.url, name: file.name });
      toast("success", "Uploaded.");
    });
    xhr.addEventListener("error", function () {
      delete root.dataset.busy;
      uploadProgress(root, null);
      toast("error", "Upload failed — check your connection and retry.");
      if (done) done(false);
    });
    xhr.send(fd);
  }

  /* ---- Several files in one field ------------------------------------------ */
  /*
   * The column holds a JSON array of storage paths, so the list in the DOM and
   * that array are the same thing said twice; the array is rebuilt from the
   * list after every change rather than edited in parallel, which is what keeps
   * them from drifting.
   *
   * Uploads run one at a time. Ten parallel requests would race for the one
   * progress bar and give the server a burst it has no reason to take.
   */

  function isMulti(root) { return root && root.dataset.multiple === "1"; }

  function syncFileList(root) {
    var list = root.querySelector("[data-steward-upload-list]");
    var hidden = root.querySelector("[data-steward-upload-value]");
    if (!list || !hidden) return;
    var paths = [];
    list.querySelectorAll("[data-steward-upload-item]").forEach(function (li) {
      paths.push(li.dataset.path);
    });
    hidden.value = paths.length ? JSON.stringify(paths) : "";
    list.hidden = paths.length === 0;

    var max = parseInt(root.dataset.maxFiles || "10", 10);
    var pick = root.querySelector("[data-steward-upload-pick]");
    if (pick) pick.hidden = paths.length >= max;
  }

  function addFileRow(root, file) {
    var list = root.querySelector("[data-steward-upload-list]");
    if (!list) return;
    var li = document.createElement("li");
    li.className = "steward-upload-current";
    li.dataset.stewardUploadItem = "";
    li.dataset.path = file.path;

    if (root.dataset.kind === "images") {
      var img = document.createElement("img");
      img.className = "steward-upload-thumb";
      img.dataset.stewardUploadThumb = "";
      img.src = file.url || "";
      img.alt = "";
      li.appendChild(img);
    } else {
      var icon = document.createElement("span");
      icon.className = "steward-upload-icon";
      icon.innerHTML = '<svg class="size-4 lucide" xmlns="http://www.w3.org/2000/svg" ' +
        'width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" ' +
        'stroke-width="2" stroke-linecap="round" stroke-linejoin="round">' +
        '<path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z"/>' +
        '<path d="M14 2v4a2 2 0 0 0 2 2h4"/></svg>';
      li.appendChild(icon);
    }

    var meta = document.createElement("span");
    meta.className = "steward-upload-meta";
    var link = document.createElement("a");
    link.className = "steward-upload-name";
    link.href = file.url || "";
    link.target = "_blank";
    link.rel = "noopener";
    link.textContent = file.name || file.path;
    var note = document.createElement("span");
    note.className = "steward-upload-note";
    note.dataset.stewardUploadNote = "";
    note.hidden = true;
    meta.appendChild(link);
    meta.appendChild(note);
    li.appendChild(meta);

    var remove = document.createElement("button");
    remove.type = "button";
    remove.className = "btn";
    remove.dataset.variant = "ghost";
    remove.dataset.size = "icon-xs";
    remove.dataset.stewardUploadRemove = "";
    remove.setAttribute("aria-label", "Remove " + (file.name || file.path));
    remove.innerHTML = '<svg class="lucide" xmlns="http://www.w3.org/2000/svg" width="24" ' +
      'height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" ' +
      'stroke-linecap="round" stroke-linejoin="round"><path d="M18 6 6 18"/>' +
      '<path d="m6 6 12 12"/></svg>';
    li.appendChild(remove);

    list.appendChild(li);
    syncFileList(root);
  }

  // acceptFiles is the one entry point for a picked or dropped set, whichever
  // kind of field received it.
  function acceptFiles(root, fileList) {
    if (!root) return;
    var files = Array.prototype.slice.call(fileList);
    if (!isMulti(root)) {
      sendUpload(root, files[0]);
      return;
    }
    var list = root.querySelector("[data-steward-upload-list]");
    var held = list ? list.querySelectorAll("[data-steward-upload-item]").length : 0;
    var max = parseInt(root.dataset.maxFiles || "10", 10);
    var room = max - held;
    if (room <= 0) {
      toast("error", "This field holds at most " + max + " files.");
      return;
    }
    if (files.length > room) {
      toast("warning", "Only " + room + " more file(s) fit here; the rest were skipped.");
      files = files.slice(0, room);
    }
    uploadQueue(root, files);
  }

  function uploadQueue(root, files) {
    var i = 0;
    function next() {
      if (i >= files.length) return;
      var file = files[i++];
      sendUpload(root, file, function (ok, out) {
        if (ok) addFileRow(root, { path: out.path, url: out.url, name: file.name });
        next();
      });
    }
    next();
  }

  document.addEventListener("click", function (e) {
    var pick = e.target.closest("[data-steward-upload-pick]");
    if (pick) {
      var root = pick.closest("[data-steward-upload]");
      var input = root && root.querySelector("[data-steward-upload-input]");
      if (input) input.click();
      return;
    }
    var remove = e.target.closest("[data-steward-upload-remove]");
    if (remove) {
      var r = remove.closest("[data-steward-upload]");
      var inp = r && r.querySelector("[data-steward-upload-input]");
      if (inp) inp.value = "";
      var item = remove.closest("[data-steward-upload-item]");
      if (item) {
        item.remove();
        syncFileList(r);
      } else {
        showUpload(r, null);
      }
    }
  });

  document.addEventListener("change", function (e) {
    var input = e.target.closest("[data-steward-upload-input]");
    if (!input || !input.files || input.files.length === 0) return;
    acceptFiles(input.closest("[data-steward-upload]"), input.files);
  });

  // Dropping anywhere on the field, rather than only on the input the native
  // control would have offered.
  ["dragenter", "dragover"].forEach(function (type) {
    document.addEventListener(type, function (e) {
      var root = e.target.closest("[data-steward-upload]");
      if (!root || root.dataset.disabled === "1") return;
      e.preventDefault();
      root.dataset.dropping = "1";
    });
  });
  ["dragleave", "drop"].forEach(function (type) {
    document.addEventListener(type, function (e) {
      var root = e.target.closest("[data-steward-upload]");
      if (!root) return;
      if (type === "dragleave" && root.contains(e.relatedTarget)) return;
      delete root.dataset.dropping;
      if (type !== "drop") return;
      e.preventDefault();
      if (root.dataset.disabled === "1") return;
      var files = e.dataTransfer && e.dataTransfer.files;
      if (files && files.length) acceptFiles(root, files);
    });
  });

  // A thumbnail that fails to load is the only signal the file is gone.
  document.addEventListener("error", function (e) {
    var thumb = e.target.closest && e.target.closest("[data-steward-upload-thumb]");
    if (!thumb || !thumb.getAttribute("src")) return;
    markUploadMissing(thumb.closest("[data-steward-upload]"));
  }, true);

  /* ---- Rich text editor ------------------------------------------------------- */
  /*
   * Progressive enhancement over the Richtext field's textarea: the textarea
   * stays the element that submits, and a contenteditable surface edits its
   * value. With JavaScript off, or if this fails, the raw HTML is still
   * editable in the textarea.
   *
   * Markup is sanitized server-side on save (sanitize.go), so nothing here is a
   * security boundary — execCommand's output only has to be tidy, not trusted.
   */

  // icon names are Lucide symbols in the vendored sprite, the same source the
  // rest of the panel draws from. state marks the tools that can report whether
  // they are on, which is what the pressed styling reads.
  var RICHTEXT_TOOLS = [
    { cmd: "bold", icon: "bold", title: "Bold (⌘B)", state: true },
    { cmd: "italic", icon: "italic", title: "Italic (⌘I)", state: true },
    { cmd: "underline", icon: "underline", title: "Underline (⌘U)", state: true },
    { cmd: "strikeThrough", icon: "strikethrough", title: "Strikethrough", state: true },
    { sep: true },
    { cmd: "formatBlock", arg: "h2", icon: "heading-2", title: "Heading 2" },
    { cmd: "formatBlock", arg: "h3", icon: "heading-3", title: "Heading 3" },
    { cmd: "formatBlock", arg: "p", icon: "pilcrow", title: "Paragraph" },
    { sep: true },
    { cmd: "insertUnorderedList", icon: "list", title: "Bulleted list", state: true },
    { cmd: "insertOrderedList", icon: "list-ordered", title: "Numbered list", state: true },
    { cmd: "formatBlock", arg: "blockquote", icon: "text-quote", title: "Quote" },
    { sep: true },
    { cmd: "createLink", icon: "link", title: "Insert link" },
    { cmd: "unlink", icon: "unlink", title: "Remove link" },
    { cmd: "insertImage", icon: "image-plus", title: "Insert image" },
    { cmd: "removeFormat", icon: "remove-formatting", title: "Clear formatting" }
  ];

  // queryCommandState answers for inline commands only. A block command has to
  // be compared against the block the caret sits in, which is what
  // queryCommandValue reports.
  function richtextActive(t) {
    try {
      if (t.cmd === "formatBlock") {
        var block = String(document.queryCommandValue("formatBlock") || "").toLowerCase();
        return block === t.arg;
      }
      if (t.state) return document.queryCommandState(t.cmd);
    } catch (err) {
      return false;
    }
    return false;
  }

  function buildRichtext(wrap) {
    if (wrap.dataset.stewardRichtextReady === "1") return;
    var ta = wrap.querySelector("textarea");
    if (!ta) return;
    // A disabled or read-only field keeps the plain textarea.
    if (wrap.dataset.disabled === "1" || wrap.dataset.readonly === "1") {
      wrap.dataset.stewardRichtextReady = "1";
      return;
    }
    wrap.dataset.stewardRichtextReady = "1";

    var sprite = wrap.dataset.sprite || "";
    var uploadURL = wrap.dataset.upload || "";
    var buttons = [];

    var bar = document.createElement("div");
    bar.className = "steward-richtext-toolbar";
    bar.setAttribute("role", "toolbar");
    bar.setAttribute("aria-label", "Formatting");

    var surface = document.createElement("div");
    surface.className = "steward-richtext-surface";
    surface.contentEditable = "true";
    surface.setAttribute("role", "textbox");
    surface.setAttribute("aria-multiline", "true");
    // The visible label points at the now-hidden textarea, so copy its text
    // onto the surface — otherwise the editor has no accessible name.
    var label = ta.id && document.querySelector('label[for="' + ta.id + '"]');
    if (label) surface.setAttribute("aria-label", label.textContent.trim().replace(/\*$/, ""));
    // Left to itself a contenteditable puts the first line in no block at all
    // and separates the rest with <div>, so paragraph spacing never applies and
    // the paragraph button never lights. Seed a block and ask for <p>.
    try { document.execCommand("defaultParagraphSeparator", false, "p"); } catch (err) { /* older engine */ }
    surface.innerHTML = ta.value.trim() === "" ? "<p><br></p>" : ta.value;

    RICHTEXT_TOOLS.forEach(function (t) {
      if (t.sep) {
        var s = document.createElement("span");
        s.className = "steward-richtext-sep";
        s.setAttribute("aria-hidden", "true");
        bar.appendChild(s);
        return;
      }
      var b = document.createElement("button");
      b.type = "button";
      b.className = "steward-richtext-btn";
      b.title = t.title;
      b.setAttribute("aria-label", t.title);
      b.setAttribute("aria-pressed", "false");
      b.innerHTML = iconGlyph(sprite, t.icon);
      buttons.push({ tool: t, el: b });
      b.addEventListener("mousedown", function (e) {
        // Keep focus (and the selection) in the surface.
        e.preventDefault();
      });
      b.addEventListener("click", function (e) {
        e.preventDefault();
        surface.focus();
        if (t.cmd === "createLink") {
          var url = window.prompt("Link URL", "https://");
          if (!url) return;
          document.execCommand("createLink", false, url);
        } else if (t.cmd === "insertImage") {
          pickImage();
          return;
        } else if (t.cmd === "formatBlock") {
          document.execCommand("formatBlock", false, t.arg);
        } else {
          document.execCommand(t.cmd, false, null);
        }
        sync();
        refreshState();
      });
      bar.appendChild(b);
    });

    // The seeded block must not read as content, or a Required field would be
    // satisfied by an empty editor.
    function sync() {
      var empty = surface.textContent.trim() === "" && !surface.querySelector("img");
      ta.value = empty ? "" : surface.innerHTML;
    }

    // Reads the caret's formatting back onto the toolbar, so the buttons say
    // what the text at the caret already is.
    function refreshState() {
      for (var i = 0; i < buttons.length; i++) {
        buttons[i].el.setAttribute("aria-pressed", richtextActive(buttons[i].tool) ? "true" : "false");
      }
    }

    // Uploads through the field's own endpoint, which applies the same size and
    // type limits an Image field gets, then drops an <img> at the caret. The
    // selection is captured first: opening the file dialog takes focus, and a
    // collapsed range cannot be recovered afterwards.
    function pickImage() {
      if (!uploadURL) return;
      var range = null;
      var sel = window.getSelection();
      if (sel && sel.rangeCount && surface.contains(sel.anchorNode)) {
        range = sel.getRangeAt(0).cloneRange();
      }
      var input = document.createElement("input");
      input.type = "file";
      input.accept = "image/*";
      input.className = "hidden";
      input.addEventListener("change", function () {
        var file = input.files && input.files[0];
        input.remove();
        if (!file) return;
        var body = new FormData();
        body.append("file", file);
        wrap.setAttribute("data-busy", "1");
        fetch(uploadURL, {
          method: "POST",
          body: body,
          headers: { "X-CSRF-Token": csrfToken() },
          credentials: "same-origin"
        })
          .then(function (r) { return r.json().then(function (j) { return { ok: r.ok, body: j }; }); })
          .then(function (res) {
            if (!res.ok || !res.body || !res.body.url) {
              throw new Error((res.body && res.body.message) || "Upload failed.");
            }
            insertImage(res.body.url, file.name);
          })
          .catch(function (err) { window.alert(err.message || "Upload failed."); })
          .finally(function () { wrap.removeAttribute("data-busy"); });
      });
      document.body.appendChild(input);
      input.click();

      function insertImage(url, alt) {
        surface.focus();
        var s = window.getSelection();
        if (range && s) { s.removeAllRanges(); s.addRange(range); }
        var img = document.createElement("img");
        img.src = url;
        img.alt = alt || "";
        var r = s && s.rangeCount ? s.getRangeAt(0) : null;
        if (r && surface.contains(r.commonAncestorContainer)) {
          r.deleteContents();
          r.insertNode(img);
          r.setStartAfter(img);
          r.collapse(true);
          s.removeAllRanges();
          s.addRange(r);
        } else {
          surface.appendChild(img);
        }
        sync();
      }
    }

    surface.addEventListener("input", sync);
    surface.addEventListener("blur", sync);
    surface.addEventListener("keyup", refreshState);
    surface.addEventListener("mouseup", refreshState);
    surface.addEventListener("focus", refreshState);
    // The caret can also move without a key or a click — undo, or a command run
    // from the toolbar of another editor on the same page.
    document.addEventListener("selectionchange", function () {
      if (surface.contains(document.getSelection && document.getSelection().anchorNode)) refreshState();
    });
    // Paste as plain text: pasted Word and web markup is mostly attributes the
    // server would strip anyway, and dropping it here keeps the surface honest
    // about what will be saved.
    surface.addEventListener("paste", function (e) {
      if (!e.clipboardData) return;
      e.preventDefault();
      var text = e.clipboardData.getData("text/plain");
      document.execCommand("insertText", false, text);
      sync();
    });
    // The form may be submitted without the surface ever blurring.
    var form = ta.form;
    if (form) form.addEventListener("submit", sync);

    ta.classList.add("hidden");
    wrap.insertBefore(bar, ta);
    wrap.insertBefore(surface, ta);
  }

  function initRichtext() {
    var nodes = document.querySelectorAll("[data-steward-richtext]");
    for (var i = 0; i < nodes.length; i++) buildRichtext(nodes[i]);
  }

  document.addEventListener("DOMContentLoaded", initRichtext);
  document.addEventListener("htmx:afterSettle", initRichtext);

  /* ---- Markdown editor -------------------------------------------------------- */
  /*
   * A Write/Preview pair over the Markdown field's textarea. The preview is
   * rendered by the server, not in the browser: the detail view renders with
   * goldmark and the same allowlist, and a second parser here would eventually
   * disagree with it. The textarea remains the field, so with JavaScript off
   * this is the plain control it was.
   */

  function buildMarkdown(wrap) {
    if (wrap.dataset.stewardMarkdownReady === "1") return;
    var ta = wrap.querySelector("textarea");
    if (!ta || !wrap.dataset.render) return;
    wrap.dataset.stewardMarkdownReady = "1";

    var tabs = document.createElement("div");
    tabs.className = "steward-markdown-tabs";
    tabs.setAttribute("role", "tablist");

    var view = document.createElement("div");
    view.className = "steward-markdown-preview prose-sm max-w-none";
    view.hidden = true;

    var write = tabButton("Write", true);
    var preview = tabButton("Preview", false);
    tabs.appendChild(write);
    tabs.appendChild(preview);

    function tabButton(label, on) {
      var b = document.createElement("button");
      b.type = "button";
      b.className = "steward-markdown-tab";
      b.textContent = label;
      b.setAttribute("role", "tab");
      b.setAttribute("aria-selected", on ? "true" : "false");
      return b;
    }

    function show(previewing) {
      write.setAttribute("aria-selected", previewing ? "false" : "true");
      preview.setAttribute("aria-selected", previewing ? "true" : "false");
      ta.hidden = previewing;
      view.hidden = !previewing;
    }

    write.addEventListener("click", function () { show(false); ta.focus(); });
    preview.addEventListener("click", function () {
      show(true);
      if (ta.value.trim() === "") {
        view.innerHTML = '<p class="text-muted-foreground">Nothing to preview.</p>';
        return;
      }
      view.setAttribute("aria-busy", "true");
      var body = new URLSearchParams();
      body.set("value", ta.value);
      fetch(wrap.dataset.render, {
        method: "POST",
        body: body,
        headers: {
          "Content-Type": "application/x-www-form-urlencoded",
          "X-CSRF-Token": csrfToken()
        },
        credentials: "same-origin"
      })
        .then(function (r) {
          if (!r.ok) throw new Error("Preview failed.");
          return r.text();
        })
        // The response is the server's sanitized HTML, the same string the
        // detail view renders.
        .then(function (html) { view.innerHTML = html; })
        .catch(function () {
          view.innerHTML = '<p class="text-destructive">Could not render a preview.</p>';
        })
        .finally(function () { view.removeAttribute("aria-busy"); });
    });

    wrap.insertBefore(tabs, ta);
    wrap.appendChild(view);
  }

  function initMarkdown() {
    var nodes = document.querySelectorAll("[data-steward-markdown]");
    for (var i = 0; i < nodes.length; i++) buildMarkdown(nodes[i]);
  }

  document.addEventListener("DOMContentLoaded", initMarkdown);
  document.addEventListener("htmx:afterSettle", initMarkdown);

  /* ---- Command palette -------------------------------------------------------- */
  /*
   * Basecoat's command component owns the filtering, the arrow keys and the
   * Enter. This opens the dialog, follows the chosen entry through htmx so the
   * page swaps rather than reloads, and labels the shortcut with the key the
   * reader's own platform uses.
   */

  function commandDialog() { return document.getElementById("steward-command"); }

  function openCommand() {
    var dlg = commandDialog();
    if (!dlg || dlg.open) return;
    dlg.showModal();
    var input = dlg.querySelector("header input");
    if (input) {
      input.value = "";
      // The component filters on input, so an empty one has to be announced to
      // clear a filter left behind by the previous opening.
      input.dispatchEvent(new Event("input", { bubbles: true }));
      input.focus();
    }
    renderCommandResults([]);
  }

  // One listener for the whole palette: the input is replaced on no page, so
  // this can bind once.
  document.addEventListener("input", function (e) {
    var dlg = commandDialog();
    if (!dlg || !dlg.open || commandRefiltering) return;
    if (!e.target.closest("#steward-command header")) return;
    var query = e.target.value;
    clearTimeout(commandTimer);
    commandTimer = setTimeout(function () { searchCommand(query); }, COMMAND_DEBOUNCE_MS);
    // The static entries filter instantly; only the fetch waits.
    updateCommandEmpty();
  });

  // --- searching records -------------------------------------------------------
  // The static entries are filtered in the browser by Basecoat. Records cannot
  // be: there are more of them than belong in a page, and which ones a reader
  // may see is the server's to decide. So the palette asks as it types.

  var COMMAND_DEBOUNCE_MS = 220;
  var commandTimer = null;
  var commandAbort = null;
  // Set while the filter is re-run over freshly injected items: that dispatch
  // is an input event like any other, and without this it would schedule the
  // fetch that produced it, for ever.
  var commandRefiltering = false;

  function commandResultsBox() {
    var dlg = commandDialog();
    return dlg && dlg.querySelector("[data-steward-command-results]");
  }

  // A menuitem the component filters like any other. data-filter carries the
  // text to match, so a fetched row is searchable by the same rules.
  function commandItem(r, sprite) {
    var el = document.createElement("div");
    el.setAttribute("role", "menuitem");
    el.setAttribute("data-steward-goto", r.url);
    el.setAttribute("data-filter", (r.title || "") + " " + (r.subtitle || ""));
    if (r.icon && sprite) {
      var glyph = document.createElement("span");
      glyph.innerHTML = iconGlyph(sprite, r.icon);
      el.appendChild(glyph.firstChild);
    }
    var title = document.createElement("span");
    title.className = "steward-command-title";
    title.textContent = r.title || "";
    title.title = r.title || "";
    el.appendChild(title);
    if (r.subtitle) {
      var sub = document.createElement("span");
      // data-shortcut is the component's own trailing slot (ms-auto), so the
      // second line sits where a keyboard hint would rather than fighting the
      // title for the row.
      sub.setAttribute("data-shortcut", "");
      sub.className = "steward-command-sub";
      sub.textContent = r.subtitle;
      el.appendChild(sub);
    }
    return el;
  }

  function renderCommandResults(results) {
    var box = commandResultsBox();
    if (!box) return;
    box.innerHTML = "";
    var groups = [];
    var byGroup = {};
    results.forEach(function (r) {
      if (!byGroup[r.group]) { byGroup[r.group] = []; groups.push(r.group); }
      byGroup[r.group].push(r);
    });
    var sprite = box.getAttribute("data-sprite") || "";
    groups.forEach(function (name) {
      var g = document.createElement("div");
      g.setAttribute("role", "group");
      g.setAttribute("aria-label", name);
      var h = document.createElement("h3");
      // The component styles headings by role, not by tag.
      h.setAttribute("role", "heading");
      h.setAttribute("aria-level", "3");
      h.textContent = name;
      g.appendChild(h);
      byGroup[name].forEach(function (r) { g.appendChild(commandItem(r, sprite)); });
      box.appendChild(g);
    });
    box.hidden = results.length === 0;
    // refresh, not an input event: the component keeps the items it knows in
    // its own state, and filtering only re-judges that list. Rows arriving from
    // a fetch are absent from it, so they never become "visible" as far as the
    // component is concerned — which is why hovering one highlighted nothing
    // and the arrow keys walked straight past them.
    commandRefiltering = true;
    if (window.basecoat && window.basecoat.refresh) {
      window.basecoat.refresh(box.closest(".command"));
    }
    commandRefiltering = false;
    updateCommandEmpty();
  }

  // Shown when neither the pages nor the records matched — otherwise a typo
  // leaves an empty box with nothing to say.
  function updateCommandEmpty() {
    var dlg = commandDialog();
    if (!dlg) return;
    var note = dlg.querySelector("[data-steward-command-empty]");
    if (!note) return;
    var visible = [...dlg.querySelectorAll('[role="menuitem"]')].some(function (i) {
      return i.offsetParent !== null;
    });
    var typed = (dlg.querySelector("header input") || {}).value || "";
    note.hidden = visible || typed.trim() === "";
  }

  function searchCommand(query) {
    if (commandAbort) commandAbort.abort();
    if (query.trim().length < 2) {
      renderCommandResults([]);
      return;
    }
    commandAbort = new AbortController();
    fetch(stewardPrefix() + "/_command?q=" + encodeURIComponent(query), {
      credentials: "same-origin",
      signal: commandAbort.signal
    })
      .then(function (r) { return r.ok ? r.json() : { results: [] }; })
      .then(function (body) { renderCommandResults(body.results || []); })
      .catch(function (err) {
        if (err && err.name === "AbortError") return;
        console.error("steward: command search", err);
      });
  }

  // The panel can be mounted anywhere, so the prefix is read off a link the
  // layout always renders rather than assumed to be /admin.
  function stewardPrefix() {
    var el = document.querySelector("[data-steward-goto]");
    var uri = el && el.getAttribute("data-steward-goto");
    if (!uri) return "";
    return uri.replace(/\/[^/]*$/, "");
  }

  document.addEventListener("keydown", function (e) {
    if ((e.metaKey || e.ctrlKey) && (e.key === "k" || e.key === "K")) {
      e.preventDefault();
      var dlg = commandDialog();
      dlg && dlg.open ? dlg.close() : openCommand();
    }
  });

  document.addEventListener("click", function (e) {
    if (e.target.closest && e.target.closest("[data-steward-command]")) {
      e.preventDefault();
      openCommand();
      return;
    }
    var item = e.target.closest && e.target.closest("[data-steward-goto]");
    if (!item) return;
    var url = item.getAttribute("data-steward-goto");
    var dlg = commandDialog();
    if (dlg && dlg.open) dlg.close();
    if (window.htmx) {
      window.htmx.ajax("GET", url, {
        target: "#page-content", swap: "innerHTML scroll:#page-content:top"
      }).then(function () { window.history.pushState({}, "", url); });
    } else {
      window.location.href = url;
    }
  });

  // ⌘ on a Mac, Ctrl everywhere else. The markup says Ctrl so that a page
  // rendered without scripting still says something true.
  document.addEventListener("DOMContentLoaded", function () {
    if (!/Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)) return;
    document.querySelectorAll("[data-steward-cmdkey]").forEach(function (el) {
      el.textContent = "⌘";
    });
  });

  /* ---- Copy to clipboard ------------------------------------------------------ */
  /*
   * Delegated, so it covers grid cells rendered by htmx as well as the detail
   * page. The value copied is the attribute's, which is what is stored — not
   * the cell's text, which may be truncated, formatted, or a badge.
   */

  var COPY_HELD_MS = 1200;

  function flashCopied(btn) {
    btn.setAttribute("data-copied", "1");
    setTimeout(function () { btn.removeAttribute("data-copied"); }, COPY_HELD_MS);
  }

  // Clipboard access needs a secure context; a panel served over plain http on
  // anything but localhost has none, so fall back to a detached selection.
  function writeClipboard(text) {
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text);
    }
    return new Promise(function (resolve, reject) {
      var ta = document.createElement("textarea");
      ta.value = text;
      ta.setAttribute("readonly", "");
      ta.style.position = "fixed";
      ta.style.opacity = "0";
      document.body.appendChild(ta);
      ta.select();
      var ok = false;
      try { ok = document.execCommand("copy"); } catch (err) { ok = false; }
      ta.remove();
      ok ? resolve() : reject(new Error("copy refused"));
    });
  }

  document.addEventListener("click", function (e) {
    var btn = e.target.closest && e.target.closest("[data-steward-copy]");
    if (!btn) return;
    e.preventDefault();
    e.stopPropagation();
    writeClipboard(btn.getAttribute("data-steward-copy") || "")
      .then(function () {
        flashCopied(btn);
        toast("success", "Copied to clipboard.");
      })
      .catch(function () { toast("error", "Could not copy to the clipboard."); });
  });

  /* ---- Colour field ----------------------------------------------------------- */
  /*
   * The text input is the field; the swatch is a way of filling it in. A native
   * colour control has no empty state — hand it "" and it reports #000000 — so
   * it is never what submits, and an untouched field stays empty.
   */

  var HEX = /^#[0-9a-fA-F]{6}$/;

  function buildColor(wrap) {
    if (wrap.dataset.stewardColorReady === "1") return;
    var swatch = wrap.querySelector("[data-steward-color-swatch]");
    var text = wrap.querySelector("[data-steward-color-text]");
    if (!swatch || !text) return;
    wrap.dataset.stewardColorReady = "1";
    var clear = wrap.querySelector("[data-steward-color-clear]");

    function syncSwatch() {
      var v = text.value.trim();
      wrap.dataset.empty = v === "" ? "1" : "";
      if (HEX.test(v)) swatch.value = v.toLowerCase();
    }
    syncSwatch();

    swatch.addEventListener("input", function () {
      text.value = swatch.value.toLowerCase();
      wrap.dataset.empty = "";
      text.dispatchEvent(new Event("change", { bubbles: true }));
    });
    text.addEventListener("input", syncSwatch);
    if (clear) {
      clear.addEventListener("click", function () {
        text.value = "";
        syncSwatch();
        text.focus();
        text.dispatchEvent(new Event("change", { bubbles: true }));
      });
    }
  }

  function initColor() {
    var nodes = document.querySelectorAll("[data-steward-color]");
    for (var i = 0; i < nodes.length; i++) buildColor(nodes[i]);
  }

  document.addEventListener("DOMContentLoaded", initColor);
  document.addEventListener("htmx:afterSettle", initColor);

  /* ---- Icon picker ------------------------------------------------------------ */
  /*
   * Enhancement over the field's <select>, which stays the thing that submits —
   * with scripting off it is still a usable control. Here it is hidden behind a
   * collapsed trigger showing the current glyph, and a popover grid that only
   * builds when first opened.
   *
   * Glyphs are <use> references into the vendored sprite, so all ~1,600 icons
   * cost one cached request rather than 1,600 inline SVGs in the page.
   */

  var ICON_SVG_ATTRS =
    'viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" ' +
    'stroke-linecap="round" stroke-linejoin="round"';

  function iconGlyph(sprite, name) {
    return '<svg class="lucide" ' + ICON_SVG_ATTRS + ' aria-hidden="true">' +
      '<use href="' + sprite + "#" + name + '"/></svg>';
  }

  function buildIconPicker(wrap) {
    if (wrap.dataset.stewardIconpickerReady === "1") return;
    var select = wrap.querySelector("[data-iconpicker-select]");
    if (!select) return;
    if (select.disabled) { wrap.dataset.stewardIconpickerReady = "1"; return; }
    wrap.dataset.stewardIconpickerReady = "1";

    var sprite = wrap.dataset.sprite || "";
    var names = [];
    for (var i = 0; i < select.options.length; i++) {
      var v = select.options[i].value;
      if (v) names.push(v);
    }

    var trigger = document.createElement("button");
    trigger.type = "button";
    trigger.className = "steward-iconpicker-trigger";
    trigger.setAttribute("aria-expanded", "false");
    trigger.setAttribute("aria-haspopup", "true");

    var panel = document.createElement("div");
    panel.className = "steward-iconpicker-panel";
    panel.hidden = true;

    var filter = document.createElement("input");
    filter.type = "search";
    filter.className = "input steward-iconpicker-filter";
    filter.placeholder = "Search " + names.length + " icons…";
    filter.setAttribute("aria-label", "Search icons");

    var grid = document.createElement("div");
    grid.className = "steward-iconpicker-grid";

    var status = document.createElement("p");
    status.className = "steward-iconpicker-status";

    panel.appendChild(filter);
    panel.appendChild(grid);
    panel.appendChild(status);

    function paintTrigger() {
      var name = select.value;
      trigger.innerHTML =
        (name ? iconGlyph(sprite, name) : '<span class="steward-iconpicker-none">—</span>') +
        '<span class="steward-iconpicker-name">' + (name || "No icon") + "</span>" +
        '<span class="steward-iconpicker-caret" aria-hidden="true"></span>';
      // The server-rendered preview is only needed until the trigger exists.
      var preview = wrap.querySelector("[data-iconpicker-preview]");
      if (preview) preview.remove();
    }

    function choose(name) {
      select.value = name;
      // Let anything listening on the field (validation, dirty tracking) see it.
      select.dispatchEvent(new Event("change", { bubbles: true }));
      paintTrigger();
      close();
      trigger.focus();
    }

    // The grid is built once, on first open: 1,600 buttons is not something to
    // spend on a field nobody has touched.
    var built = false;
    function render(q) {
      var frag = document.createDocumentFragment();
      var shown = 0;
      var none = document.createElement("button");
      none.type = "button";
      none.className = "steward-iconpicker-item";
      none.title = "No icon";
      none.innerHTML = '<span class="steward-iconpicker-none">—</span>';
      none.addEventListener("click", function () { choose(""); });
      if (!q) frag.appendChild(none);

      for (var i = 0; i < names.length; i++) {
        var name = names[i];
        if (q && name.indexOf(q) === -1) continue;
        var b = document.createElement("button");
        b.type = "button";
        b.className = "steward-iconpicker-item";
        b.title = name;
        b.dataset.name = name;
        if (name === select.value) b.setAttribute("aria-current", "true");
        b.innerHTML = iconGlyph(sprite, name);
        b.addEventListener("click", function (e) {
          choose(e.currentTarget.dataset.name);
        });
        frag.appendChild(b);
        shown++;
      }
      grid.textContent = "";
      grid.appendChild(frag);
      status.textContent = shown === 0
        ? "No icon matches “" + q + "”."
        : shown + " of " + names.length;
    }

    function open() {
      if (!built) { render(""); built = true; }
      panel.hidden = false;
      trigger.setAttribute("aria-expanded", "true");
      filter.value = "";
      filter.focus();
      var current = grid.querySelector('[aria-current="true"]');
      if (current) current.scrollIntoView({ block: "nearest" });
    }

    function close() {
      panel.hidden = true;
      trigger.setAttribute("aria-expanded", "false");
    }

    trigger.addEventListener("click", function () {
      if (panel.hidden) open(); else close();
    });

    var debounce;
    filter.addEventListener("input", function () {
      clearTimeout(debounce);
      debounce = setTimeout(function () {
        render(filter.value.trim().toLowerCase());
      }, 80);
    });
    // Enter in a search field would otherwise submit the form.
    filter.addEventListener("keydown", function (e) {
      if (e.key === "Enter") e.preventDefault();
    });

    wrap.addEventListener("keydown", function (e) {
      if (e.key === "Escape" && !panel.hidden) {
        e.stopPropagation();
        close();
        trigger.focus();
      }
    });
    document.addEventListener("click", function (e) {
      if (!panel.hidden && !wrap.contains(e.target)) close();
    });

    select.classList.add("hidden");
    wrap.appendChild(trigger);
    wrap.appendChild(panel);
    paintTrigger();
  }

  function initIconPickers() {
    var nodes = document.querySelectorAll("[data-steward-iconpicker]");
    for (var i = 0; i < nodes.length; i++) buildIconPicker(nodes[i]);
  }

  document.addEventListener("DOMContentLoaded", initIconPickers);
  document.addEventListener("htmx:afterSettle", initIconPickers);

  /* ---- Multi-select: suggestions from the server ---------------------------- */
  /*
   * The field renders one page of options and fetches the rest as the reader
   * types, because a list of several thousand costs more to ship and parse than
   * the whole rest of the page.
   *
   * The component is told the filtering is not its own (data-filter="manual"),
   * so it shows whatever the listbox holds; this replaces those children and
   * asks it to rescan. Selected entries are left in place: the component reads
   * a chip's label from the stored selection, but an option still in the list
   * is what lets it be unticked.
   */

  // Short enough that an ordinary typing pace gets the list updating as it
  // goes, long enough that a fast burst is still one request. The endpoint
  // answers in about 50ms, so waiting longer buys nothing.
  var OPTION_DEBOUNCE_MS = 120;

  function optionEl(opt, selected) {
    var el = document.createElement("div");
    el.setAttribute("role", "option");
    el.dataset.value = opt.value;
    el.dataset.label = opt.label;
    // Readable in full even when the field's width truncates it.
    el.title = opt.label;
    if (selected) el.setAttribute("aria-selected", "true");
    el.textContent = opt.label;
    return el;
  }

  function selectedValues(root) {
    var hidden = root.querySelector('input[type="hidden"]');
    if (!hidden || !hidden.value) return [];
    try {
      var parsed = JSON.parse(hidden.value);
      if (!Array.isArray(parsed)) return [];
      return parsed.map(function (e) {
        return e && typeof e === "object" ? String(e.value) : String(e);
      });
    } catch (err) {
      return [];
    }
  }

  function fillOptions(root, listbox, opts, more) {
    var held = selectedValues(root);
    var frag = document.createDocumentFragment();
    var seen = {};
    opts.forEach(function (o) {
      seen[String(o.value)] = true;
      frag.appendChild(optionEl(o, held.indexOf(String(o.value)) >= 0));
    });
    // Keep what is already chosen reachable even when it does not match the
    // query, so a chip can always be unticked from the list it came from.
    root.querySelectorAll('[role="option"][aria-selected="true"]').forEach(function (el) {
      if (!seen[el.dataset.value]) {
        frag.appendChild(optionEl({ value: el.dataset.value, label: el.dataset.label }, true));
      }
    });
    listbox.replaceChildren(frag);
    listbox.dataset.empty = more
      ? "Too many matches — keep typing."
      : (opts.length === 0 ? "No matches." : "");

    // refresh() re-applies the stored selection, and for a multiple combobox
    // that empties the query box — which is right after picking a chip and
    // wrong here, where the reader is still typing. Only the single-select path
    // puts the text back, so this does.
    var input = root.querySelector('input[role="combobox"]');
    var typed = input ? input.value : null;
    var at = input ? input.selectionStart : null;
    if (typeof root.refresh === "function") root.refresh();
    if (input && typed !== null && input.value !== typed) {
      input.value = typed;
      if (at !== null && input.setSelectionRange) input.setSelectionRange(at, at);
    }
  }

  function initOptionSearch(root) {
    if (root.dataset.stewardOptionsBound === "1") return;
    var url = root.dataset.stewardOptions;
    var input = root.querySelector('input[role="combobox"]');
    var listbox = root.querySelector('[role="listbox"]');
    if (!url || !input || !listbox) return;
    root.dataset.stewardOptionsBound = "1";

    var timer = null;
    var inFlight = null;
    var lastQuery = null;

    function load(query) {
      if (query === lastQuery) return;
      lastQuery = query;
      // A reply that arrives after a later one would show stale suggestions.
      if (inFlight) inFlight.abort();
      var ctrl = new AbortController();
      inFlight = ctrl;
      fetch(url + "&q=" + encodeURIComponent(query), {
        headers: { Accept: "application/json" },
        credentials: "same-origin",
        signal: ctrl.signal,
      })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (body) {
          if (!body || ctrl.signal.aborted) return;
          fillOptions(root, listbox, body.options || [], !!body.more);
        })
        .catch(function () { /* the list keeps what it has */ });
    }

    input.addEventListener("input", function () {
      clearTimeout(timer);
      timer = setTimeout(function () { load(input.value.trim()); }, OPTION_DEBOUNCE_MS);
    });
    // Opening asks for an unfiltered page, never for input.value: on a single
    // select that holds the current selection's label, and searching for it
    // would offer the reader only what they have already chosen.
    input.addEventListener("focus", function () { load(""); }, { once: true });
  }

  function initOptionSearches() {
    document.querySelectorAll("[data-steward-options]").forEach(initOptionSearch);
  }

  document.addEventListener("DOMContentLoaded", initOptionSearches);
  document.addEventListener("htmx:afterSettle", initOptionSearches);

  /* ---- Grid: horizontal scroll state --------------------------------------- */
  /*
   * Flags a table container that is scrolled away from its trailing edge, which
   * is when the pinned actions column actually overlaps something and its
   * divider should show. Without this the divider is a permanent line on tables
   * that fit the window and never scroll.
   */

  function syncHScroll(el) {
    // How far the trailing edge is from the end of the content. Compared with a
    // pixel of slack because fractional layout widths rarely land on zero.
    var trailing = el.scrollWidth - el.clientWidth - Math.abs(el.scrollLeft);
    el.dataset.hscroll = trailing > 1 ? "1" : "0";
  }

  function initHScroll() {
    document.querySelectorAll("[data-steward-hscroll]").forEach(function (el) {
      syncHScroll(el);
      if (el.dataset.hscrollBound === "1") return;
      el.dataset.hscrollBound = "1";
      el.addEventListener("scroll", function () { syncHScroll(el); }, { passive: true });
    });
  }

  document.addEventListener("DOMContentLoaded", initHScroll);
  document.addEventListener("htmx:afterSettle", initHScroll);
  // Column show/hide and window resizing both change whether it overflows.
  window.addEventListener("resize", initHScroll);

  /* ---- Grouped header rows -------------------------------------------------- */
  /*
   * Grouped columns render a second header row, which has to stick below the
   * first rather than on top of it. The offset is the first row's height, which
   * comes from the style pack's cell padding, so it is measured here and handed
   * to the stylesheet.
   */

  function setHeaderRowOffset() {
    document.querySelectorAll("[data-steward-grid]").forEach(function (table) {
      var rows = table.querySelectorAll("thead > tr");
      if (rows.length < 2) return;
      table.style.setProperty("--steward-header-row-h", rows[0].offsetHeight + "px");
    });
  }

  document.addEventListener("DOMContentLoaded", setHeaderRowOffset);
  document.addEventListener("htmx:afterSettle", setHeaderRowOffset);
  window.addEventListener("resize", setHeaderRowOffset);

  /* ---- Row action menus escaping the table's clip -------------------------- */
  /*
   * Basecoat places a dropdown's popover with offsets against its wrapper, and
   * the grid's scroll container sets overflow-x — which per CSS also makes
   * overflow-y compute to auto — so a row menu left there would be clipped on
   * both axes. The stylesheet takes these popovers out of the container by
   * making them fixed; that leaves their offsets resolving against the viewport,
   * which is what this recomputes from the trigger's own rect.
   *
   * Menus are closed on scroll rather than tracked, because one that follows its
   * row while the table moves under the cursor is worse than one that gets out
   * of the way.
   */

  function placeRowMenu(pop) {
    var wrap = pop.closest("[data-steward-menu]");
    var trigger = wrap && wrap.querySelector("[aria-haspopup]");
    if (!trigger) return;

    // Reset to the stylesheet's own placement before measuring, so a previous
    // open's pinned width is not what gets measured.
    pop.style.cssText = "";
    var r = trigger.getBoundingClientRect();

    // [data-popover] carries min-width:100%, which against a fixed box's
    // containing block is the whole viewport. Pinned to the width it actually
    // has so nothing can stretch it there.
    var w = pop.offsetWidth;
    var h = pop.offsetHeight;
    pop.style.minWidth = w + "px";

    pop.style.margin = "0";
    // Basecoat positions with logical offsets (data-align="end" is
    // inset-inline-end), which map onto the same physical properties set below.
    // Cleared first so the physical values that follow are what apply.
    pop.style.insetInlineStart = "auto";
    pop.style.insetInlineEnd = "auto";
    pop.style.insetBlockStart = "auto";
    pop.style.insetBlockEnd = "auto";

    // Aligned to the trigger's trailing edge, then kept inside the viewport.
    var left = Math.min(Math.max(8, r.right - w), window.innerWidth - w - 8);
    var top = r.bottom + 4;
    // Flip above when there is not room below but there is above.
    if (top + h > window.innerHeight - 8 && r.top - h - 4 > 8) top = r.top - h - 4;

    pop.style.left = left + "px";
    pop.style.top = top + "px";
    pop.style.right = "auto";
    pop.style.bottom = "auto";

    // WebKit does not focus a button when it is clicked, so a menu opened with
    // the mouse there left focus on the body — and the component binds its keys
    // to the wrapper, so arrows and Escape did nothing. The menu pattern wants
    // focus on the trigger regardless. preventScroll matters: the trigger sits
    // in a scroll container, and scrolling it into view would close the menu.
    if (!wrap.contains(document.activeElement)) {
      trigger.focus({ preventScroll: true });
    }
  }

  function closeOpenRowMenus(e) {
    // A tall menu scrolling its own overflow is not the table moving under it.
    var t = e && e.target;
    if (t && t.closest && t.closest("[data-steward-menu] > [data-popover]")) return;
    document.querySelectorAll('[data-steward-menu] [data-popover][aria-hidden="false"]')
      .forEach(function (pop) {
        var wrap = pop.closest("[data-steward-menu]");
        // Ask the component to close, so it restores focus and aria itself.
        if (wrap && typeof wrap.close === "function") wrap.close(false);
      });
  }

  // Basecoat flips aria-hidden when it opens; that is the signal. The placement
  // is left in place on close, since the popover stays visible through its fade
  // and resetting it there would snap the menu back to the trigger first.
  var rowMenuObserver = new MutationObserver(function (records) {
    records.forEach(function (rec) {
      if (rec.target.getAttribute("aria-hidden") === "false") placeRowMenu(rec.target);
    });
  });

  function initRowMenus() {
    document.querySelectorAll("[data-steward-menu] > [data-popover]").forEach(function (pop) {
      if (pop.dataset.stewardMenuBound === "1") return;
      pop.dataset.stewardMenuBound = "1";
      rowMenuObserver.observe(pop, { attributes: true, attributeFilter: ["aria-hidden"] });
    });
  }

  document.addEventListener("DOMContentLoaded", initRowMenus);
  document.addEventListener("htmx:afterSettle", initRowMenus);
  // A fixed menu no longer belongs to its row once anything moves.
  window.addEventListener("resize", closeOpenRowMenus);
  window.addEventListener("scroll", closeOpenRowMenus, true);

  /* ---- Date picker, on pointer devices only ---------------------------------- */
  /*
   * The native input stays the field. On a touch device nothing here runs at
   * all: the reader gets the picker their platform already gives them, which is
   * better than anything worth writing here. On a pointer device a calendar is
   * added beside it, because Safari's desktop control for datetime-local is a
   * row of steppers with no calendar at all.
   *
   * Everything is written through the input's own value in ISO, so what is
   * submitted is exactly what it was before.
   */

  var COARSE = window.matchMedia && window.matchMedia("(pointer: coarse)").matches;

  function pad(n) { return (n < 10 ? "0" : "") + n; }

  // isoDate and isoTime are the halves of what a date or datetime input holds.
  function isoDate(d) {
    return d.getFullYear() + "-" + pad(d.getMonth() + 1) + "-" + pad(d.getDate());
  }

  function readInput(input) {
    var raw = input.value;
    var datePart = raw.slice(0, 10);
    var timePart = raw.length > 10 ? raw.slice(11) : "";
    var d = null;
    if (/^\d{4}-\d{2}-\d{2}$/.test(datePart)) {
      var p = datePart.split("-");
      d = new Date(+p[0], +p[1] - 1, +p[2]);
    }
    return { date: d, time: timePart };
  }

  function writeInput(input, date, time) {
    var v = isoDate(date);
    if (input.type === "datetime-local") {
      v += "T" + (time || "00:00:00");
    }
    input.value = v;
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.dispatchEvent(new Event("change", { bubbles: true }));
  }

  function monthLabel(d) {
    var lang = document.documentElement.lang || undefined;
    try {
      return new Intl.DateTimeFormat(lang, { month: "long", year: "numeric" }).format(d);
    } catch (err) {
      return d.getFullYear() + "-" + pad(d.getMonth() + 1);
    }
  }

  function weekdayNames() {
    var lang = document.documentElement.lang || undefined;
    var out = [];
    // 2024-01-01 was a Monday; the grid starts on Monday.
    for (var i = 0; i < 7; i++) {
      var d = new Date(2024, 0, 1 + i);
      try {
        out.push(new Intl.DateTimeFormat(lang, { weekday: "narrow" }).format(d));
      } catch (err) {
        out.push(["M", "T", "W", "T", "F", "S", "S"][i]);
      }
    }
    return out;
  }

  function decadeStart(year) { return year - ((year % 12) + 12) % 12; }

  function shortMonth(m) {
    var lang = document.documentElement.lang || undefined;
    try {
      return new Intl.DateTimeFormat(lang, { month: "short" }).format(new Date(2024, m, 1));
    } catch (err) {
      return String(m + 1);
    }
  }

  // readBounds takes the range off the input's own min and max, so the calendar
  // refuses exactly what the control and the server would.
  function readBounds(input) {
    function parse(v) {
      if (!v) return null;
      var d = v.slice(0, 10).split("-");
      if (d.length !== 3) return null;
      return new Date(+d[0], +d[1] - 1, +d[2]);
    }
    return { min: parse(input.getAttribute("min")), max: parse(input.getAttribute("max")) };
  }

  function dayOutOfBounds(day, b) {
    if (b.min && day < b.min) return true;
    if (b.max && day > b.max) return true;
    return false;
  }

  function monthOutOfBounds(year, month, b) {
    var last = new Date(year, month + 1, 0);
    var first = new Date(year, month, 1);
    if (b.min && last < b.min) return true;
    if (b.max && first > b.max) return true;
    return false;
  }

  function yearOutOfBounds(year, b) {
    if (b.min && year < b.min.getFullYear()) return true;
    if (b.max && year > b.max.getFullYear()) return true;
    return false;
  }

  function buildCalendar(root, input) {
    var cal = document.createElement("div");
    cal.className = "steward-cal";
    cal.setAttribute("role", "dialog");
    cal.setAttribute("aria-label", "Choose a date");

    var state = readInput(input);
    var view = new Date(state.date || new Date());
    view.setDate(1);
    var mode = "day";
    var bounds = readBounds(input);

    function render() {
      cal.replaceChildren();

      var head = document.createElement("div");
      head.className = "steward-cal-head";
      var prev = navButton("‹", "Previous", function () { step(-1); });
      var title = document.createElement("button");
      title.type = "button";
      title.className = "steward-cal-title";
      title.textContent = headingFor();
      // Stepping a month at a time is fine for next week and hopeless for three
      // years ago, so the heading zooms out: days to months to years.
      title.setAttribute("aria-label", "Change " + (mode === "day" ? "month" : mode));
      title.addEventListener("click", function () {
        mode = mode === "day" ? "month" : "year";
        render();
      });
      var next = navButton("›", "Next", function () { step(1); });
      head.append(prev, title, next);
      cal.appendChild(head);

      if (mode === "month") { cal.appendChild(monthGrid()); return; }
      if (mode === "year") { cal.appendChild(yearGrid()); return; }

      var grid = document.createElement("div");
      grid.className = "steward-cal-grid";
      weekdayNames().forEach(function (n) {
        var el = document.createElement("span");
        el.className = "steward-cal-dow";
        el.setAttribute("aria-hidden", "true");
        el.textContent = n;
        grid.appendChild(el);
      });

      // Monday-first: how far back the first cell reaches.
      var first = new Date(view.getFullYear(), view.getMonth(), 1);
      var lead = (first.getDay() + 6) % 7;
      var start = new Date(first);
      start.setDate(1 - lead);
      var selected = readInput(input).date;
      var today = new Date();

      for (var i = 0; i < 42; i++) {
        var day = new Date(start.getFullYear(), start.getMonth(), start.getDate() + i);
        var btn = document.createElement("button");
        btn.type = "button";
        btn.className = "steward-cal-day";
        btn.textContent = String(day.getDate());
        btn.dataset.iso = isoDate(day);
        if (day.getMonth() !== view.getMonth()) btn.dataset.outside = "1";
        if (isoDate(day) === isoDate(today)) btn.dataset.today = "1";
        if (selected && isoDate(day) === isoDate(selected)) {
          btn.setAttribute("aria-selected", "true");
        }
        if (dayOutOfBounds(day, bounds)) btn.disabled = true;
        grid.appendChild(btn);
      }
      cal.appendChild(grid);

      var foot = document.createElement("div");
      foot.className = "steward-cal-foot";
      if (input.type === "datetime-local") {
        var time = document.createElement("input");
        time.type = "time";
        time.step = "1";
        time.className = "steward-cal-time";
        time.value = readInput(input).time || "00:00:00";
        time.addEventListener("change", function () {
          var cur = readInput(input).date || new Date();
          writeInput(input, cur, time.value);
        });
        foot.appendChild(time);
      }
      var todayBtn = document.createElement("button");
      todayBtn.type = "button";
      todayBtn.className = "steward-cal-today";
      todayBtn.textContent = "Today";
      todayBtn.addEventListener("click", function () {
        var t = new Date();
        writeInput(input, t, input.type === "datetime-local"
          ? pad(t.getHours()) + ":" + pad(t.getMinutes()) + ":" + pad(t.getSeconds())
          : "");
        close(root);
      });
      foot.appendChild(todayBtn);
      cal.appendChild(foot);
    }

    function headingFor() {
      if (mode === "day") return monthLabel(view);
      if (mode === "month") return String(view.getFullYear());
      var base = decadeStart(view.getFullYear());
      return base + " – " + (base + 11);
    }

    function monthGrid() {
      var grid = document.createElement("div");
      grid.className = "steward-cal-grid steward-cal-months";
      for (var m = 0; m < 12; m++) {
        var b = document.createElement("button");
        b.type = "button";
        b.className = "steward-cal-cell";
        b.textContent = shortMonth(m);
        b.dataset.month = String(m);
        if (m === view.getMonth()) b.setAttribute("aria-selected", "true");
        if (monthOutOfBounds(view.getFullYear(), m, bounds)) b.disabled = true;
        grid.appendChild(b);
      }
      grid.addEventListener("click", function (e) {
        var cell = e.target.closest("[data-month]");
        if (!cell || cell.disabled) return;
        view.setMonth(+cell.dataset.month);
        mode = "day";
        render();
      });
      return grid;
    }

    function yearGrid() {
      var grid = document.createElement("div");
      grid.className = "steward-cal-grid steward-cal-months";
      var base = decadeStart(view.getFullYear());
      for (var i = 0; i < 12; i++) {
        var y = base + i;
        var b = document.createElement("button");
        b.type = "button";
        b.className = "steward-cal-cell";
        b.textContent = String(y);
        b.dataset.year = String(y);
        if (y === view.getFullYear()) b.setAttribute("aria-selected", "true");
        if (yearOutOfBounds(y, bounds)) b.disabled = true;
        grid.appendChild(b);
      }
      grid.addEventListener("click", function (e) {
        var cell = e.target.closest("[data-year]");
        if (!cell || cell.disabled) return;
        view.setFullYear(+cell.dataset.year);
        mode = "month";
        render();
      });
      return grid;
    }

    function navButton(glyph, label, fn) {
      var b = document.createElement("button");
      b.type = "button";
      b.className = "steward-cal-nav";
      b.textContent = glyph;
      b.setAttribute("aria-label", label);
      b.addEventListener("click", fn);
      return b;
    }

    function step(n) {
      if (mode === "day") view.setMonth(view.getMonth() + n);
      else if (mode === "month") view.setFullYear(view.getFullYear() + n);
      else view.setFullYear(view.getFullYear() + n * 12);
      render();
      // The button that was just pressed no longer exists; its replacement
      // takes the focus so the month can be stepped again.
      var navs = cal.querySelectorAll(".steward-cal-nav");
      var replacement = months < 0 ? navs[0] : navs[navs.length - 1];
      if (replacement) replacement.focus();
    }

    cal.addEventListener("click", function (e) {
      var day = e.target.closest(".steward-cal-day");
      if (!day || day.disabled) return;
      var p = day.dataset.iso.split("-");
      writeInput(input, new Date(+p[0], +p[1] - 1, +p[2]), readInput(input).time);
      close(root);
      input.focus();
    });

    cal.addEventListener("keydown", function (e) {
      var day = e.target.closest(".steward-cal-day");
      if (!day) return;
      var moves = { ArrowLeft: -1, ArrowRight: 1, ArrowUp: -7, ArrowDown: 7 };
      if (moves[e.key] === undefined) return;
      e.preventDefault();
      var p = day.dataset.iso.split("-");
      var d = new Date(+p[0], +p[1] - 1, +p[2] + moves[e.key]);
      if (d.getMonth() !== view.getMonth()) {
        view = new Date(d.getFullYear(), d.getMonth(), 1);
        render();
      }
      var target = cal.querySelector('.steward-cal-day[data-iso="' + isoDate(d) + '"]');
      if (target) target.focus();
    });

    render();
    return cal;
  }

  function close(root) {
    var cal = root.querySelector(".steward-cal");
    if (cal) cal.remove();
    var trigger = root.querySelector(".steward-datepicker-trigger");
    if (trigger) trigger.setAttribute("aria-expanded", "false");
  }

  function initDatePickers() {
    if (COARSE) return;
    document.querySelectorAll("[data-steward-datepicker]").forEach(function (root) {
      if (root.dataset.stewardCalReady === "1") return;
      var input = root.querySelector("input");
      if (!input || input.disabled || input.readOnly) return;
      root.dataset.stewardCalReady = "1";

      var trigger = document.createElement("button");
      trigger.type = "button";
      trigger.className = "steward-datepicker-trigger";
      trigger.setAttribute("aria-label", "Open the calendar");
      trigger.setAttribute("aria-expanded", "false");
      trigger.innerHTML = '<svg class="size-4" xmlns="http://www.w3.org/2000/svg" width="24" ' +
        'height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" ' +
        'stroke-linecap="round" stroke-linejoin="round"><path d="M8 2v4"/><path d="M16 2v4"/>' +
        '<rect width="18" height="18" x="3" y="4" rx="2"/><path d="M3 10h18"/></svg>';
      trigger.addEventListener("click", function () {
        if (root.querySelector(".steward-cal")) { close(root); return; }
        document.querySelectorAll("[data-steward-datepicker]").forEach(close);
        var cal = buildCalendar(root, input);
        root.appendChild(cal);
        // Open upwards where there is no room below, which is most of the time
        // for the last field on a form.
        var r = cal.getBoundingClientRect();
        if (r.bottom > window.innerHeight - 8 && root.getBoundingClientRect().top > r.height + 8) {
          cal.style.top = "auto";
          cal.style.bottom = "calc(100% + 0.25rem)";
        }
        trigger.setAttribute("aria-expanded", "true");
        var sel = root.querySelector('.steward-cal-day[aria-selected="true"]') ||
          root.querySelector('.steward-cal-day[data-today="1"]');
        if (sel) sel.focus();
      });
      root.appendChild(trigger);
    });
  }

  document.addEventListener("DOMContentLoaded", initDatePickers);
  document.addEventListener("htmx:afterSettle", initDatePickers);
  // mousedown, not click: changing month replaces the calendar's children, so by
  // the time a click on the month arrows reaches this handler the button it came
  // from is detached and closest() can no longer find the picker it sits in —
  // which read as a click outside, and closed the thing being navigated.
  document.addEventListener("mousedown", function (e) {
    if (e.target.closest("[data-steward-datepicker]")) return;
    document.querySelectorAll("[data-steward-datepicker]").forEach(close);
  });
  document.addEventListener("keydown", function (e) {
    if (e.key !== "Escape") return;
    document.querySelectorAll("[data-steward-datepicker]").forEach(close);
  });

  /* ---- Date range filter ------------------------------------------------------ */
  /*
   * One calendar for both ends of a range. The two hidden inputs it writes are
   * the same ones a Between filter submits, so nothing on the server knows the
   * difference and a URL typed by hand still works.
   *
   * On a coarse pointer the control falls back to the platform's two date
   * inputs, for the same reason the single picker does.
   */

  function rangeLabel(from, to) {
    var lang = document.documentElement.lang || undefined;
    var fmt = function (iso) {
      var p = iso.split("-");
      var d = new Date(+p[0], +p[1] - 1, +p[2]);
      try {
        return d.toLocaleDateString(lang, { day: "numeric", month: "short", year: "numeric" });
      } catch (err) {
        return iso;
      }
    };
    if (from && to) return from === to ? fmt(from) : fmt(from) + " – " + fmt(to);
    if (from) return "From " + fmt(from);
    if (to) return "Until " + fmt(to);
    return "Any date";
  }

  function buildRangeCalendar(root, from, to, onPick) {
    var cal = document.createElement("div");
    cal.className = "steward-cal steward-cal-range";
    var view = new Date(from ? from + "T00:00:00" : Date.now());
    // pending holds a first click waiting for its second.
    var pending = null;
    var hover = null;

    function within(iso, a, b) {
      if (!a || !b) return false;
      var lo = a < b ? a : b, hi = a < b ? b : a;
      return iso > lo && iso < hi;
    }

    function render() {
      cal.innerHTML = "";
      var head = document.createElement("div");
      head.className = "steward-cal-head";
      var prev = document.createElement("button");
      prev.type = "button";
      prev.className = "steward-cal-nav";
      prev.textContent = "‹";
      prev.setAttribute("aria-label", "Previous month");
      prev.addEventListener("click", function () {
        view = new Date(view.getFullYear(), view.getMonth() - 1, 1);
        render();
      });
      var title = document.createElement("span");
      title.className = "steward-cal-title";
      title.textContent = shortMonth(view.getMonth()) + " " + view.getFullYear();
      var next = document.createElement("button");
      next.type = "button";
      next.className = "steward-cal-nav";
      next.textContent = "›";
      next.setAttribute("aria-label", "Next month");
      next.addEventListener("click", function () {
        view = new Date(view.getFullYear(), view.getMonth() + 1, 1);
        render();
      });
      head.append(prev, title, next);
      cal.appendChild(head);

      var grid = document.createElement("div");
      grid.className = "steward-cal-grid";
      weekdayNames().forEach(function (n) {
        var el = document.createElement("span");
        el.className = "steward-cal-dow";
        el.setAttribute("aria-hidden", "true");
        el.textContent = n;
        grid.appendChild(el);
      });

      var first = new Date(view.getFullYear(), view.getMonth(), 1);
      var lead = (first.getDay() + 6) % 7;
      var start = new Date(first);
      start.setDate(1 - lead);
      var today = isoDate(new Date());

      for (var i = 0; i < 42; i++) {
        var day = new Date(start.getFullYear(), start.getMonth(), start.getDate() + i);
        var iso = isoDate(day);
        var btn = document.createElement("button");
        btn.type = "button";
        btn.className = "steward-cal-day";
        btn.textContent = String(day.getDate());
        btn.dataset.iso = iso;
        if (day.getMonth() !== view.getMonth()) btn.dataset.outside = "1";
        if (iso === today) btn.dataset.today = "1";

        var lo = pending || from, hi = pending ? (hover || pending) : to;
        if (iso === lo || iso === hi) btn.setAttribute("aria-selected", "true");
        if (within(iso, lo, hi)) btn.dataset.inRange = "1";
        grid.appendChild(btn);
      }
      cal.appendChild(grid);

      var foot = document.createElement("div");
      foot.className = "steward-cal-foot";
      [["This month", 0], ["Last 7 days", 7], ["Last 30 days", 30]].forEach(function (p) {
        var b = document.createElement("button");
        b.type = "button";
        b.className = "steward-cal-preset";
        b.textContent = p[0];
        b.addEventListener("click", function () {
          var now = new Date();
          if (p[1] === 0) {
            onPick(isoDate(new Date(now.getFullYear(), now.getMonth(), 1)),
              isoDate(new Date(now.getFullYear(), now.getMonth() + 1, 0)));
            return;
          }
          var back = new Date(now.getFullYear(), now.getMonth(), now.getDate() - p[1] + 1);
          onPick(isoDate(back), isoDate(now));
        });
        foot.appendChild(b);
      });
      cal.appendChild(foot);
    }

    cal.addEventListener("click", function (e) {
      var day = e.target.closest(".steward-cal-day");
      if (!day) return;
      var iso = day.dataset.iso;
      if (!pending) {
        pending = iso;
        hover = null;
        render();
        return;
      }
      // Second click completes it, in whichever order the two were chosen.
      var a = pending, b = iso;
      pending = null;
      onPick(a < b ? a : b, a < b ? b : a);
    });
    cal.addEventListener("mouseover", function (e) {
      if (!pending) return;
      var day = e.target.closest(".steward-cal-day");
      if (!day || day.dataset.iso === hover) return;
      hover = day.dataset.iso;
      render();
    });

    render();
    return cal;
  }

  function closeRange(root) {
    var cal = root.querySelector(".steward-cal");
    if (cal) cal.remove();
    var t = root.querySelector("[data-steward-daterange-trigger]");
    if (t) t.setAttribute("aria-expanded", "false");
  }

  function initDateRanges() {
    document.querySelectorAll("[data-steward-daterange]").forEach(function (root) {
      if (root.dataset.stewardRangeReady === "1") return;
      root.dataset.stewardRangeReady = "1";

      var fromInput = root.querySelector("[data-steward-daterange-from]");
      var toInput = root.querySelector("[data-steward-daterange-to]");
      var label = root.querySelector("[data-steward-daterange-label]");
      var clear = root.querySelector("[data-steward-daterange-clear]");
      var trigger = root.querySelector("[data-steward-daterange-trigger]");

      function paint() {
        label.textContent = rangeLabel(fromInput.value, toInput.value);
        root.dataset.set = fromInput.value || toInput.value ? "1" : "";
        if (clear) clear.hidden = !(fromInput.value || toInput.value);
      }
      paint();

      // Without a fine pointer, two native inputs beat a calendar built here.
      if (COARSE) {
        trigger.hidden = true;
        [["from", fromInput], ["to", toInput]].forEach(function (pair) {
          var d = document.createElement("input");
          d.type = "date";
          d.className = "input";
          d.value = pair[1].value;
          d.setAttribute("aria-label", pair[0] === "from" ? "From" : "Until");
          d.addEventListener("change", function () { pair[1].value = d.value; paint(); });
          root.insertBefore(d, clear);
        });
        return;
      }

      trigger.addEventListener("click", function () {
        if (root.querySelector(".steward-cal")) { closeRange(root); return; }
        document.querySelectorAll("[data-steward-daterange]").forEach(closeRange);
        var cal = buildRangeCalendar(root, fromInput.value, toInput.value, function (a, b) {
          fromInput.value = a;
          toInput.value = b;
          paint();
          closeRange(root);
        });
        root.appendChild(cal);
        var r = cal.getBoundingClientRect();
        if (r.bottom > window.innerHeight - 8 && root.getBoundingClientRect().top > r.height + 8) {
          cal.style.top = "auto";
          cal.style.bottom = "calc(100% + 0.25rem)";
        }
        trigger.setAttribute("aria-expanded", "true");
      });

      if (clear) {
        clear.addEventListener("click", function () {
          fromInput.value = "";
          toInput.value = "";
          paint();
          closeRange(root);
        });
      }
    });
  }

  document.addEventListener("DOMContentLoaded", initDateRanges);
  document.addEventListener("htmx:afterSettle", initDateRanges);
  // mousedown for the same reason the single picker uses it: rendering replaces
  // the calendar's children, so a click has nothing left to trace back to.
  document.addEventListener("mousedown", function (e) {
    if (e.target.closest("[data-steward-daterange]")) return;
    document.querySelectorAll("[data-steward-daterange]").forEach(closeRange);
  });
  document.addEventListener("keydown", function (e) {
    if (e.key !== "Escape") return;
    document.querySelectorAll("[data-steward-daterange]").forEach(closeRange);
  });

  /* ---- Theme toggle ----------------------------------------------------------- */

  document.addEventListener("click", function (e) {
    var btn = e.target.closest("[data-steward-theme-toggle]");
    if (!btn) return;
    e.preventDefault();
    var dark = document.documentElement.classList.toggle("dark");
    document.cookie = "steward_theme=" + (dark ? "dark" : "light") + "; path=/; max-age=31536000; samesite=lax";
  });

  window.Steward = { toast: toast, handleEnvelope: handleEnvelope, request: request };
})();
