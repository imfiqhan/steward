/* Steward UI bundle: htmx + Basecoat components + admin glue.
 * Built by tools/assets (esbuild, pure Go) into assets/dist/app.js —
 * no Node at runtime. Basecoat's core watches the DOM with a
 * MutationObserver, so htmx swaps initialize themselves. */
import htmx from "../vendor/htmx/htmx.esm.js";
import "../vendor/basecoat/js/all.js";

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
        preview.classList.remove("hidden");
        var img = preview.querySelector("img");
        if (img) { img.src = out.url; }
        else { preview.innerHTML = '<a href="' + out.url + '" target="_blank" rel="noopener" class="underline">Uploaded file</a>'; }
      }
      toast("success", "Uploaded.");
    }).catch(function () {
      toast("error", "Upload failed — check your connection and retry.");
    }).finally(function () { input.disabled = false; });
  });

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

  var RICHTEXT_TOOLS = [
    { cmd: "bold", label: "B", title: "Bold (⌘B)", cls: "font-semibold" },
    { cmd: "italic", label: "I", title: "Italic (⌘I)", cls: "italic" },
    { cmd: "underline", label: "U", title: "Underline (⌘U)", cls: "underline" },
    { sep: true },
    { cmd: "formatBlock", arg: "h2", label: "H2", title: "Heading 2" },
    { cmd: "formatBlock", arg: "h3", label: "H3", title: "Heading 3" },
    { cmd: "formatBlock", arg: "p", label: "¶", title: "Paragraph" },
    { sep: true },
    { cmd: "insertUnorderedList", label: "•", title: "Bulleted list" },
    { cmd: "insertOrderedList", label: "1.", title: "Numbered list" },
    { cmd: "formatBlock", arg: "blockquote", label: "❝", title: "Quote" },
    { sep: true },
    { cmd: "createLink", label: "🔗", title: "Insert link" },
    { cmd: "unlink", label: "⛓", title: "Remove link" },
    { cmd: "removeFormat", label: "⌫", title: "Clear formatting" }
  ];

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
    surface.innerHTML = ta.value;

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
      b.className = "steward-richtext-btn " + (t.cls || "");
      b.title = t.title;
      b.setAttribute("aria-label", t.title);
      b.textContent = t.label;
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
        } else if (t.cmd === "formatBlock") {
          document.execCommand("formatBlock", false, t.arg);
        } else {
          document.execCommand(t.cmd, false, null);
        }
        sync();
      });
      bar.appendChild(b);
    });

    function sync() { ta.value = surface.innerHTML; }

    surface.addEventListener("input", sync);
    surface.addEventListener("blur", sync);
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
