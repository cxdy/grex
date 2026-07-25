/**
 * Light/dark theme toggle. Preference in localStorage; falls back to
 * prefers-color-scheme, then dark (logo-complementary default).
 */
(function () {
  const KEY = "grex-theme";

  function systemTheme() {
    try {
      return window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
    } catch (_) {
      return "dark";
    }
  }

  function current() {
    const stored = localStorage.getItem(KEY);
    if (stored === "light" || stored === "dark") return stored;
    return systemTheme();
  }

  function apply(theme) {
    document.documentElement.setAttribute("data-theme", theme);
    const btn = document.getElementById("theme-toggle");
    if (btn) {
      btn.setAttribute("aria-label", theme === "dark" ? "Switch to light mode" : "Switch to dark mode");
      btn.title = theme === "dark" ? "Light mode" : "Dark mode";
    }
  }

  function toggle() {
    const next = current() === "dark" ? "light" : "dark";
    localStorage.setItem(KEY, next);
    apply(next);
  }

  // Apply as early as this deferred script runs (inline head script applies first paint).
  apply(current());

  document.addEventListener("DOMContentLoaded", () => {
    apply(current());
    const btn = document.getElementById("theme-toggle");
    if (btn) btn.addEventListener("click", toggle);
  });

  try {
    window.matchMedia("(prefers-color-scheme: light)").addEventListener("change", () => {
      if (!localStorage.getItem(KEY)) apply(systemTheme());
    });
  } catch (_) {}
})();
