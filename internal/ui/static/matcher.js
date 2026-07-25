/**
 * Progressive attribute matcher autocomplete (Prometheus/Grafana Explore style).
 * Phases: key → operator (= != =~ !~) → value.
 * Builds hidden <input name="match"> fields for form submit.
 */
(function () {
  const OPS = ["=", "!=", "=~", "!~"];
  const OP_RE = /^(.+?)\s*(!~|=~|!=|=)\s*(.*)$/;

  function $(sel, root) {
    return (root || document).querySelector(sel);
  }

  function parsePartial(text) {
    const t = text.trimStart();
    const m = t.match(OP_RE);
    if (!m) {
      return { phase: "key", key: t.trim(), op: "", value: "", raw: t };
    }
    const key = m[1].trim();
    const op = m[2];
    // Preserve trailing spaces after op for typing values; use capture group 3 as-is
    // after operator in original string
    const opIdx = t.indexOf(op, m[1].length);
    const afterOp = t.slice(opIdx + op.length);
    // If user just typed op with no value yet (or only spaces), still value phase
    return {
      phase: "value",
      key,
      op,
      value: afterOp.replace(/^\s*/, ""),
      valuePrefix: afterOp.replace(/^\s*/, ""),
      raw: t,
      hasSpaceAfterOp: /^\s/.test(afterOp) || afterOp === "",
    };
  }

  function formatMatcher(key, op, value) {
    // Quote regex patterns that contain spaces or special shell-ish chars for readability
    let v = value;
    if ((op === "=~" || op === "!~") && v && !/^['"]/.test(v) && /[\s#]/.test(v)) {
      v = '"' + v.replace(/\\/g, "\\\\").replace(/"/g, '\\"') + '"';
    }
    return key + " " + op + " " + v;
  }

  function initMatcher(root) {
    const input = $(".matcher-input", root);
    const suggest = $(".matcher-suggest", root);
    const chipsEl = $(".matcher-chips", root);
    const form = root.closest("form");
    if (!input || !suggest || !form) return;

    let active = -1;
    let items = [];
    let keysCache = null;

    // Seed chips from existing hidden match inputs / data attribute
    const initial = (root.dataset.matches || "")
      .split("\n")
      .map((s) => s.trim())
      .filter(Boolean);
    initial.forEach((m) => addChip(m, false));

    function syncHidden() {
      form.querySelectorAll('input[name="match"][data-matcher-chip]').forEach((el) => el.remove());
      chipsEl.querySelectorAll(".matcher-chip").forEach((chip) => {
        const hidden = document.createElement("input");
        hidden.type = "hidden";
        hidden.name = "match";
        hidden.value = chip.dataset.match;
        hidden.setAttribute("data-matcher-chip", "1");
        form.appendChild(hidden);
      });
    }

    function addChip(matcher, clearInput) {
      if (!matcher) return;
      // de-dupe
      const esc =
        window.CSS && CSS.escape
          ? CSS.escape(matcher)
          : matcher.replace(/\\/g, "\\\\").replace(/"/g, '\\"');
      if (chipsEl.querySelector('[data-match="' + esc + '"]')) {
        if (clearInput) input.value = "";
        return;
      }
      const chip = document.createElement("button");
      chip.type = "button";
      chip.className = "matcher-chip";
      chip.dataset.match = matcher;
      chip.title = "Click to edit";
      chip.setAttribute("aria-label", "Edit filter " + matcher);
      chip.innerHTML =
        '<span class="matcher-chip-text"></span>' +
        '<span class="matcher-chip-remove" role="button" tabindex="0" aria-label="Remove filter">×</span>';
      chip.querySelector(".matcher-chip-text").textContent = matcher;

      chip.addEventListener("click", (e) => {
        // Remove button handles its own action
        if (e.target.closest(".matcher-chip-remove")) return;
        editChip(chip);
      });
      const removeEl = chip.querySelector(".matcher-chip-remove");
      removeEl.addEventListener("click", (e) => {
        e.preventDefault();
        e.stopPropagation();
        chip.remove();
        syncHidden();
      });
      removeEl.addEventListener("keydown", (e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          e.stopPropagation();
          chip.remove();
          syncHidden();
        }
      });
      chipsEl.appendChild(chip);
      if (clearInput) {
        input.value = "";
        hideSuggest();
      }
      syncHidden();
    }

    /** Move a chip back into the input for editing. */
    function editChip(chip) {
      const matcher = chip.dataset.match || "";
      if (!matcher) return;
      // If the input already has a complete matcher, commit it as a chip first
      // so it is not lost when swapping in the one being edited.
      const pending = input.value.trim();
      if (pending && OP_RE.test(pending) && pending !== matcher) {
        const m = pending.match(OP_RE);
        addChip(formatMatcher(m[1].trim(), m[2], m[3]), false);
      }
      chip.remove();
      syncHidden();
      // Normalize spacing for progressive autocomplete phases
      const m = matcher.match(OP_RE);
      if (m) {
        input.value = formatMatcher(m[1].trim(), m[2], m[3]);
      } else {
        input.value = matcher;
      }
      input.focus();
      // Place caret at end so value is easy to tweak
      const len = input.value.length;
      try {
        input.setSelectionRange(len, len);
      } catch (_) {}
      refreshSuggest();
    }

    function hideSuggest() {
      suggest.hidden = true;
      suggest.innerHTML = "";
      active = -1;
      items = [];
    }

    function showSuggest(list, kind) {
      items = list;
      active = list.length ? 0 : -1;
      if (!list.length) {
        hideSuggest();
        return;
      }
      suggest.innerHTML = "";
      list.forEach((item, i) => {
        const li = document.createElement("li");
        li.className = "matcher-suggest-item" + (i === 0 ? " is-active" : "");
        li.setAttribute("role", "option");
        li.dataset.value = item;
        li.dataset.kind = kind;
        if (kind === "op") {
          li.innerHTML = '<span class="suggest-op">' + escapeHtml(item) + "</span>";
        } else {
          li.textContent = item;
        }
        li.addEventListener("mousedown", (e) => {
          e.preventDefault();
          pick(item, kind);
        });
        suggest.appendChild(li);
      });
      suggest.hidden = false;
    }

    function escapeHtml(s) {
      return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
    }

    function setActive(i) {
      active = i;
      suggest.querySelectorAll(".matcher-suggest-item").forEach((el, idx) => {
        el.classList.toggle("is-active", idx === i);
      });
      const el = suggest.querySelectorAll(".matcher-suggest-item")[i];
      if (el) el.scrollIntoView({ block: "nearest" });
    }

    function pick(item, kind) {
      const partial = parsePartial(input.value);
      if (kind === "key") {
        input.value = item + " ";
        // after key, show operators
        showSuggest(OPS, "op");
        input.focus();
        return;
      }
      if (kind === "op") {
        const key = partial.phase === "key" ? partial.key.trim() : partial.key;
        if (!key) return;
        input.value = key + " " + item + " ";
        loadValues(key, "").then((vals) => showSuggest(vals, "value"));
        input.focus();
        return;
      }
      if (kind === "value") {
        const key = partial.key;
        const op = partial.op || "=";
        const matcher = formatMatcher(key, op, item);
        addChip(matcher, true);
        input.focus();
      }
    }

    async function fetchKeys(prefix) {
      if (!keysCache) {
        const res = await fetch("/api/attributes");
        if (!res.ok) return [];
        const data = await res.json();
        keysCache = data.keys || [];
      }
      const p = (prefix || "").toLowerCase();
      if (!p) return keysCache.slice(0, 50);
      return keysCache.filter((k) => k.toLowerCase().includes(p)).slice(0, 50);
    }

    async function loadValues(key, prefix) {
      const u = new URL("/api/attributes/values", window.location.origin);
      u.searchParams.set("key", key);
      if (prefix) u.searchParams.set("prefix", prefix);
      const res = await fetch(u);
      if (!res.ok) return [];
      const data = await res.json();
      return data.values || [];
    }

    async function refreshSuggest() {
      const partial = parsePartial(input.value);
      if (partial.phase === "key") {
        // If text ends with a complete-looking key + space, offer operators
        const trimmed = input.value;
        if (keysCache) {
          const exact = keysCache.find((k) => k === partial.key.trim());
          if (exact && /\s$/.test(trimmed)) {
            showSuggest(OPS, "op");
            return;
          }
        }
        const keys = await fetchKeys(partial.key);
        showSuggest(keys, "key");
        return;
      }
      // value phase
      if (!partial.op) {
        showSuggest(OPS, "op");
        return;
      }
      // If value empty and op just chosen, show all values
      const vals = await loadValues(partial.key, partial.valuePrefix || "");
      // Also allow free-typed regex — still show suggestions
      showSuggest(vals, "value");
    }

    input.addEventListener("focus", () => {
      refreshSuggest();
    });
    input.addEventListener("input", () => {
      refreshSuggest();
    });
    input.addEventListener("keydown", (e) => {
      if (suggest.hidden) {
        if (e.key === "Enter" && input.value.trim()) {
          e.preventDefault();
          // commit free-typed matcher if parseable
          const m = input.value.trim().match(OP_RE);
          if (m) {
            addChip(formatMatcher(m[1].trim(), m[2], m[3]), true);
          }
        }
        return;
      }
      if (e.key === "ArrowDown") {
        e.preventDefault();
        if (items.length) setActive(Math.min(active + 1, items.length - 1));
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        if (items.length) setActive(Math.max(active - 1, 0));
      } else if (e.key === "Enter") {
        e.preventDefault();
        if (active >= 0 && items[active] != null) {
          const kind = suggest.querySelectorAll(".matcher-suggest-item")[active]?.dataset.kind;
          pick(items[active], kind);
        }
      } else if (e.key === "Escape") {
        hideSuggest();
      } else if (e.key === "Tab" && active >= 0 && items[active] != null) {
        e.preventDefault();
        const kind = suggest.querySelectorAll(".matcher-suggest-item")[active]?.dataset.kind;
        pick(items[active], kind);
      }
    });
    input.addEventListener("blur", () => {
      // delay so mousedown on suggestion fires first
      setTimeout(hideSuggest, 150);
    });

    // Prefetch keys
    fetchKeys("");

    // Fleet table attribute chips (and anything else) can request a filter.
    function onAddMatch(e) {
      const d = e.detail || {};
      if (!d.key) return;
      const op = d.op || "=";
      const value = d.value != null ? String(d.value) : "";
      addChip(formatMatcher(d.key, op, value), true);
      // Keep the chip bar in view when adding from a distant table row.
      root.scrollIntoView({ behavior: "smooth", block: "nearest" });
    }
    window.addEventListener("grex:add-match", onAddMatch);

    // On form submit, if input has a complete matcher, add it as a chip
    form.addEventListener("submit", () => {
      const v = input.value.trim();
      if (v && OP_RE.test(v)) {
        const m = v.match(OP_RE);
        addChip(formatMatcher(m[1].trim(), m[2], m[3]), true);
      }
      syncHidden();
    });
  }

  // Delegate clicks on fleet attribute chips (survive htmx table swaps).
  document.addEventListener("click", (e) => {
    const chip = e.target.closest(".attr-chip[data-attr-key]");
    if (!chip) return;
    e.preventDefault();
    e.stopPropagation();
    window.dispatchEvent(
      new CustomEvent("grex:add-match", {
        detail: {
          key: chip.getAttribute("data-attr-key") || "",
          value: chip.getAttribute("data-attr-value") || "",
          op: "=",
        },
      })
    );
  });

  document.addEventListener("DOMContentLoaded", () => {
    document.querySelectorAll("[data-matcher]").forEach(initMatcher);
  });
})();
