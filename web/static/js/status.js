// The public status page updates itself every minute. It fetches the page
// again and swaps the status content, the title and the tab icon, so
// nothing reloads and focus stays where it is. It pauses while the tab is
// hidden.
(function () {
  "use strict";
  var main = document.querySelector("main[data-status]");
  if (!main || !window.fetch || !window.DOMParser) return;

  var every = 60000;
  var timer = null;
  var last = Date.now();

  function swap(html) {
    var doc = new DOMParser().parseFromString(html, "text/html");
    var next = doc.querySelector("main[data-status]");
    if (!next) return;
    document.title = doc.title || document.title;
    setIcon(doc.querySelector("link[rel=icon]"));
    if (main.contains(document.activeElement)) return; // keep the user's focus
    if (next.innerHTML !== main.innerHTML) main.innerHTML = next.innerHTML;
  }

  // setIcon takes the tab icon of the fetched page when its state differs.
  // Its href has a new fragment: Safari fetches only an icon URL it has not
  // cached.
  function setIcon(next) {
    var icon = document.querySelector("link[rel=icon]");
    if (!icon || !next) return;
    var href = next.getAttribute("href");
    if (href.split("#")[0] !== icon.getAttribute("href").split("#")[0]) icon.setAttribute("href", href);
  }

  function refresh() {
    timer = null;
    last = Date.now();
    fetch(location.pathname + location.search, { credentials: "same-origin", cache: "no-store", headers: { Accept: "text/html" } })
      .then(function (r) { return r.ok ? r.text() : ""; })
      .then(function (html) { if (html) swap(html); })
      .catch(function () {})
      .then(schedule);
  }

  function schedule() {
    if (timer !== null || document.hidden) return;
    var due = Math.max(0, last + every - Date.now());
    timer = setTimeout(refresh, due);
  }

  document.addEventListener("visibilitychange", function () {
    if (document.hidden) {
      clearTimeout(timer);
      timer = null;
    } else {
      schedule();
    }
  });

  schedule();
})();
