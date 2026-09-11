// Applies the saved theme before first paint and wires the footer toggle.
// Values: "light", "dark", or nothing (follow the system setting).
(function () {
  var KEY = "theme";
  var root = document.documentElement;

  function saved() {
    try { return localStorage.getItem(KEY) || ""; } catch (e) { return ""; }
  }
  function apply(value) {
    if (value === "light" || value === "dark") {
      root.setAttribute("data-theme", value);
    } else {
      root.removeAttribute("data-theme");
    }
    var buttons = document.querySelectorAll("[data-set-theme]");
    for (var i = 0; i < buttons.length; i++) {
      var b = buttons[i];
      b.setAttribute("aria-pressed", b.getAttribute("data-set-theme") === value ? "true" : "false");
    }
  }

  apply(saved());

  document.addEventListener("click", function (e) {
    var btn = e.target.closest && e.target.closest("[data-set-theme]");
    if (!btn) return;
    var value = btn.getAttribute("data-set-theme");
    try {
      if (value) { localStorage.setItem(KEY, value); } else { localStorage.removeItem(KEY); }
    } catch (err) { /* private mode: the choice lasts for this page only */ }
    apply(value);
  });

  document.addEventListener("DOMContentLoaded", function () { apply(saved()); });
})();
