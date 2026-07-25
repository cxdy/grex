#!/usr/bin/env node
/**
 * After copying internal/ui/static/matcher.js into docs/demo/static/,
 * inject demo hooks: grexDemoListKeys/Values + re-init after SPA renders.
 */
const fs = require("fs");
const path = require("path");

const file = path.join(__dirname, "..", "docs", "demo", "static", "matcher.js");
let src = fs.readFileSync(file, "utf8");

if (src.includes("grexDemoListKeys")) {
  console.log("patch-demo-matcher: already patched");
  process.exit(0);
}

const fetchKeysOld = `    async function fetchKeys(prefix) {
      if (!keysCache) {
        const res = await fetch("/api/attributes");
        if (!res.ok) return [];
        const data = await res.json();
        keysCache = data.keys || [];
      }
      const p = (prefix || "").toLowerCase();
      if (!p) return keysCache.slice(0, 50);
      return keysCache.filter((k) => k.toLowerCase().includes(p)).slice(0, 50);
    }`;

const fetchKeysNew = `    async function fetchKeys(prefix) {
      // Static demo / offline: host page can supply keys from synthetic fleet data.
      if (typeof window.grexDemoListKeys === "function") {
        return window.grexDemoListKeys(prefix);
      }
      if (!keysCache) {
        const res = await fetch("/api/attributes");
        if (!res.ok) return [];
        const data = await res.json();
        keysCache = data.keys || [];
      }
      const p = (prefix || "").toLowerCase();
      if (!p) return keysCache.slice(0, 50);
      return keysCache.filter((k) => k.toLowerCase().includes(p)).slice(0, 50);
    }`;

const loadValuesOld = `    async function loadValues(key, prefix) {
      const u = new URL("/api/attributes/values", window.location.origin);
      u.searchParams.set("key", key);
      if (prefix) u.searchParams.set("prefix", prefix);
      const res = await fetch(u);
      if (!res.ok) return [];
      const data = await res.json();
      return data.values || [];
    }`;

const loadValuesNew = `    async function loadValues(key, prefix) {
      if (typeof window.grexDemoListValues === "function") {
        return window.grexDemoListValues(key, prefix);
      }
      const u = new URL("/api/attributes/values", window.location.origin);
      u.searchParams.set("key", key);
      if (prefix) u.searchParams.set("prefix", prefix);
      const res = await fetch(u);
      if (!res.ok) return [];
      const data = await res.json();
      return data.values || [];
    }`;

const onAddOld = `    function onAddMatch(e) {
      const d = e.detail || {};
      if (!d.key) return;
      const op = d.op || "=";
      const value = d.value != null ? String(d.value) : "";
      addChip(formatMatcher(d.key, op, value), true);
      // Keep the chip bar in view when adding from a distant table row.
      root.scrollIntoView({ behavior: "smooth", block: "nearest" });
    }`;

const onAddNew = `    function onAddMatch(e) {
      if (!root.isConnected) return;
      const d = e.detail || {};
      if (!d.key) return;
      const op = d.op || "=";
      const value = d.value != null ? String(d.value) : "";
      addChip(formatMatcher(d.key, op, value), true);
      // Keep the chip bar in view when adding from a distant table row.
      root.scrollIntoView({ behavior: "smooth", block: "nearest" });
    }`;

const bootOld = `  document.addEventListener("DOMContentLoaded", () => {
    document.querySelectorAll("[data-matcher]").forEach(initMatcher);
  });
})();`;

const bootNew = `  function grexInitMatchers() {
    document.querySelectorAll("[data-matcher]").forEach((root) => {
      if (root.dataset.matcherReady === "1") return;
      root.dataset.matcherReady = "1";
      initMatcher(root);
    });
  }

  document.addEventListener("DOMContentLoaded", grexInitMatchers);
  // Static demo re-renders the fleet form; re-scan after each paint.
  document.addEventListener("grex:demo-ready", grexInitMatchers);
  window.grexInitMatchers = grexInitMatchers;
})();`;

for (const [name, oldS, newS] of [
  ["fetchKeys", fetchKeysOld, fetchKeysNew],
  ["loadValues", loadValuesOld, loadValuesNew],
  ["onAddMatch", onAddOld, onAddNew],
  ["boot", bootOld, bootNew],
]) {
  if (!src.includes(oldS)) {
    console.error("patch-demo-matcher: failed to find block:", name);
    process.exit(1);
  }
  src = src.replace(oldS, newS);
}

fs.writeFileSync(file, src);
console.log("patch-demo-matcher: ok");
