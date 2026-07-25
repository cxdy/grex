/**
 * Lightweight YAML syntax highlighter for agent effective config.
 * No dependencies; re-runs after htmx swaps.
 */
(function () {
  function escapeHtml(s) {
    return s
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;");
  }

  function highlightLine(line) {
    // Full-line comment
    if (/^\s*#/.test(line)) {
      return '<span class="y-comment">' + escapeHtml(line) + "</span>";
    }

    let out = "";
    let i = 0;
    const n = line.length;

    // Leading whitespace
    while (i < n && (line[i] === " " || line[i] === "\t")) {
      out += line[i];
      i++;
    }

    // List dash
    if (line[i] === "-" && (i + 1 >= n || line[i + 1] === " " || line[i + 1] === "\t")) {
      out += '<span class="y-punct">-</span>';
      i++;
      while (i < n && (line[i] === " " || line[i] === "\t")) {
        out += line[i];
        i++;
      }
    }

    // Key: value  (key may be quoted)
    const rest = line.slice(i);
    const keyMatch = rest.match(/^((?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^:#\s][^:#]*?))\s*(:)\s*(.*)$/);
    if (keyMatch && !rest.startsWith("http")) {
      const key = keyMatch[1];
      const colon = keyMatch[2];
      let value = keyMatch[3];
      out += '<span class="y-key">' + escapeHtml(key) + "</span>";
      out += '<span class="y-punct">' + colon + "</span>";
      // space after colon is in value capture optionally — re-add single space for readability
      if (value.length && !/^\s/.test(value) && keyMatch[0].includes(": ")) {
        out += " ";
      } else if (/^\s/.test(value)) {
        // keep leading space from value
      } else if (value.length === 0) {
        // bare key:
      } else {
        out += " ";
      }

      // Trailing comment on value line
      let comment = "";
      const hash = value.indexOf(" #");
      if (hash >= 0) {
        comment = value.slice(hash);
        value = value.slice(0, hash);
      } else if (/^\s*#/.test(value)) {
        comment = value;
        value = "";
      }

      out += highlightValue(value);
      if (comment) {
        out += '<span class="y-comment">' + escapeHtml(comment) + "</span>";
      }
      return out;
    }

    // No key — value-only line (multiline block content, list items already handled)
    let value = rest;
    let comment = "";
    const hash = value.indexOf(" #");
    if (hash >= 0) {
      comment = value.slice(hash);
      value = value.slice(0, hash);
    }
    out += highlightValue(value);
    if (comment) {
      out += '<span class="y-comment">' + escapeHtml(comment) + "</span>";
    }
    return out;
  }

  function highlightValue(value) {
    if (value === "") return "";
    const lead = value.match(/^\s*/)?.[0] || "";
    const body = value.slice(lead.length);
    if (!body) return escapeHtml(value);

    // Quoted string
    if (
      (body.startsWith('"') && body.endsWith('"') && body.length >= 2) ||
      (body.startsWith("'") && body.endsWith("'") && body.length >= 2)
    ) {
      return escapeHtml(lead) + '<span class="y-str">' + escapeHtml(body) + "</span>";
    }

    // Boolean / null
    if (/^(true|false|True|False|TRUE|FALSE|yes|no|Yes|No|on|off)$/.test(body)) {
      return escapeHtml(lead) + '<span class="y-bool">' + escapeHtml(body) + "</span>";
    }
    if (/^(null|Null|NULL|~)$/.test(body)) {
      return escapeHtml(lead) + '<span class="y-null">' + escapeHtml(body) + "</span>";
    }

    // Number
    if (/^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?$/.test(body)) {
      return escapeHtml(lead) + '<span class="y-num">' + escapeHtml(body) + "</span>";
    }

    // Flow indicators / braces
    if (/^[\[\]{}|,]+$/.test(body)) {
      return escapeHtml(lead) + '<span class="y-punct">' + escapeHtml(body) + "</span>";
    }

    // Anchors / aliases
    if (body.startsWith("&") || body.startsWith("*")) {
      return escapeHtml(lead) + '<span class="y-anchor">' + escapeHtml(body) + "</span>";
    }

    // Block scalars
    if (body === "|" || body === ">" || body === "|-" || body === ">-" || body === "|+" || body === ">+") {
      return escapeHtml(lead) + '<span class="y-punct">' + escapeHtml(body) + "</span>";
    }

    return escapeHtml(lead) + '<span class="y-str">' + escapeHtml(body) + "</span>";
  }

  function highlightYaml(text) {
    return text.split("\n").map(highlightLine).join("\n");
  }

  function apply(root) {
    const scope = root || document;
    scope.querySelectorAll("pre.config code.language-yaml, pre.config[data-yaml]").forEach((el) => {
      if (el.dataset.highlighted === "1") return;
      const raw = el.textContent;
      el.innerHTML = highlightYaml(raw);
      el.dataset.highlighted = "1";
    });
    // bare pre.config without code wrapper
    scope.querySelectorAll("pre.config:not(:has(code))").forEach((el) => {
      if (el.dataset.highlighted === "1") return;
      const raw = el.textContent;
      el.innerHTML = '<code class="language-yaml">' + highlightYaml(raw) + "</code>";
      el.dataset.highlighted = "1";
    });
  }

  function boot() {
    apply(document);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", boot);
  } else {
    boot();
  }

  document.body && document.body.addEventListener("htmx:afterSwap", (e) => {
    apply(e.target);
  });
  // body may not exist yet when deferred
  document.addEventListener("DOMContentLoaded", () => {
    document.body.addEventListener("htmx:afterSwap", (e) => {
      apply(e.detail && e.detail.target ? e.detail.target : e.target);
    });
  });
})();
