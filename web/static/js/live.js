// Live updates over one SSE connection to /events. Pages that want it
// carry data-live on <body>. The dashboard rows, the detail page, the tab
// title and the favicon update in place. No framework.
(function () {
  "use strict";
  var body = document.body;
  if (!body.hasAttribute("data-live") || !window.EventSource) return;

  var reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  var list = document.getElementById("monitors");
  var lastLine = document.getElementById("last-line");
  var baseTitle = document.title.replace(/^\(\d+\) /, "");
  var icon = document.querySelector("link[rel=icon]");
  // The URL of an uploaded logo, "" when the built-in icon is in use.
  var logo = icon && icon.hasAttribute("data-custom") ? icon.getAttribute("href") : "";
  var logoImage = null;
  var down = 0;

  // --- Tab title and favicon ---

  function token(name) {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  }

  function favicon(color) {
    var svg = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">' +
      '<rect x="6" y="3" width="2.5" height="26" rx="1.25" fill="#6B7280"/>' +
      '<rect x="3" y="7" width="26" height="2.5" rx="1.25" fill="#6B7280"/>' +
      '<path d="M10 11h15v13l-3.75-2.4L17.5 24l-3.75-2.4L10 24z" fill="' + color + '"/></svg>';
    return "data:image/svg+xml," + encodeURIComponent(svg);
  }

  // markedLogo returns the uploaded logo with a red dot in the bottom right
  // corner as a PNG data URL, or "" until the logo has loaded.
  function markedLogo() {
    if (!logoImage) {
      logoImage = new Image();
      logoImage.onload = setIcon;
      logoImage.src = logo;
      return "";
    }
    if (!logoImage.complete) return "";
    var size = 64;
    var canvas = document.createElement("canvas");
    canvas.width = canvas.height = size;
    var ctx = canvas.getContext("2d");
    // An SVG without a size reports no natural size. Fill the square then.
    var iw = logoImage.naturalWidth || size, ih = logoImage.naturalHeight || size;
    var scale = Math.min(size / iw, size / ih);
    var w = iw * scale, h = ih * scale;
    var r = size / 5;
    try {
      ctx.drawImage(logoImage, (size - w) / 2, (size - h) / 2, w, h);
      ctx.beginPath();
      ctx.arc(size - r, size - r, r, 0, 2 * Math.PI);
      ctx.fillStyle = token("--down");
      ctx.fill();
      ctx.lineWidth = size / 32;
      ctx.strokeStyle = token("--bg");
      ctx.stroke();
      return canvas.toDataURL("image/png");
    } catch (e) {
      return "";
    }
  }

  function setIcon() {
    if (!icon) return;
    if (!logo) {
      icon.setAttribute("href", favicon(token(down > 0 ? "--down" : "--up")));
      return;
    }
    icon.setAttribute("href", (down > 0 && markedLogo()) || logo);
  }

  function setDown(n) {
    down = n;
    document.title = (n > 0 ? "(" + n + ") " : "") + baseTitle;
    setIcon();
  }
  setDown(+body.dataset.down || 0);

  // --- "Checked 12 s ago" lines. The text must match ago() in Go. ---

  function ago(s) {
    if (s < 5) return "just now";
    if (s < 60) return s + " s ago";
    if (s < 3600) return Math.floor(s / 60) + " m ago";
    if (s < 86400) return Math.floor(s / 3600) + " h ago";
    return Math.floor(s / 86400) + " d ago";
  }

  function renderAgo(el) {
    var at = +el.dataset.at;
    if (!at) return;
    var s = Math.max(0, Math.floor(Date.now() / 1000) - at);
    var verb = el.dataset.kind === "push" ? "pushed" : "checked";
    var head = el.dataset.head || "";
    el.textContent = head ? head + " · " + verb + " " + ago(s) : verb.charAt(0).toUpperCase() + verb.slice(1) + " " + ago(s);
  }

  setInterval(function () {
    var els = document.querySelectorAll("[data-at]");
    for (var i = 0; i < els.length; i++) renderAgo(els[i]);
  }, 10000);

  function flash(el) {
    if (reduced) return;
    el.classList.remove("fade-in");
    void el.offsetWidth;
    el.classList.add("fade-in");
  }

  // --- Events ---

  function onCheck(ev) {
    var el = null;
    if (list) el = list.querySelector('.mon-row[data-id="' + ev.id + '"] .mon-checked');
    if (!el && lastLine && +lastLine.dataset.monitor === ev.id) el = lastLine;
    if (!el) return;
    el.dataset.at = String(Math.floor(Date.now() / 1000));
    if (el.hasAttribute("data-head")) {
      el.dataset.head = el.dataset.kind === "push" ? "" : (ev.ok ? ev.latency + " ms" : ev.error || "failed");
    }
    renderAgo(el);
    flash(el);
  }

  function onState(ev) {
    setDown(ev.down);
    if (lastLine && +lastLine.dataset.monitor === ev.id) {
      // The tiles, the chart and the incident list all change with the
      // state. A fresh page is simpler than patching each one.
      reloadWhenVisible();
      return;
    }
    if (!list) return;
    var row = list.querySelector('.mon-row[data-id="' + ev.id + '"]');
    if (!row) return;
    var old = row.dataset.state;
    if (old === ev.state) return;
    var before = positions();
    row.dataset.state = ev.state;
    row.classList.remove("mon-" + old);
    row.classList.add("mon-" + ev.state);
    var dot = row.querySelector(":scope > .dot");
    dot.className = "dot dot-" + ev.state;
    dot.firstChild.textContent = ev.state.charAt(0).toUpperCase() + ev.state.slice(1);
    var checked = row.querySelector(".mon-checked");
    if (ev.state === "paused") {
      checked.removeAttribute("data-at");
      checked.textContent = "Paused";
    } else if (ev.state === "pending") {
      checked.removeAttribute("data-at");
      checked.textContent = "Waiting for the first " + (checked.dataset.kind === "push" ? "push" : "check");
    }
    if (ev.state === "down") {
      list.insertBefore(row, firstRow(function (r) { return r.dataset.state !== "down"; }, row));
    } else if (old === "down") {
      var pos = +row.dataset.pos;
      list.insertBefore(row, firstRow(function (r) { return r.dataset.state !== "down" && +r.dataset.pos > pos; }, row));
    }
    slide(before);
    headline();
  }

  // firstRow returns the first row other than skip that matches, or null.
  function firstRow(match, skip) {
    var rows = list.querySelectorAll(".mon-row");
    for (var i = 0; i < rows.length; i++) {
      if (rows[i] !== skip && match(rows[i])) return rows[i];
    }
    return null;
  }

  // Rows that change state slide to their new position (FLIP).
  function positions() {
    var m = new Map();
    list.querySelectorAll(".mon-row").forEach(function (r) { m.set(r, r.getBoundingClientRect().top); });
    return m;
  }

  function slide(before) {
    if (reduced || !Element.prototype.animate) return;
    list.querySelectorAll(".mon-row").forEach(function (r) {
      var was = before.get(r);
      if (was === undefined) return;
      var d = was - r.getBoundingClientRect().top;
      if (!d) return;
      r.animate([{ transform: "translateY(" + d + "px)" }, { transform: "none" }], { duration: 150, easing: "ease-out" });
    });
  }

  // headline rebuilds the dashboard sentence. It must match headline() in Go.
  function headline() {
    var h = document.querySelector(".dash-head h1");
    if (!h) return;
    var down = 0, pending = 0, active = 0;
    list.querySelectorAll(".mon-row").forEach(function (r) {
      var s = r.dataset.state;
      if (s === "down") down++;
      if (s === "pending") pending++;
      if (s !== "paused") active++;
    });
    var text, state;
    if (down > 0) {
      text = down === 1 ? "1 monitor is down" : down + " monitors are down";
      state = "down";
    } else if (active === 0) {
      text = "All monitors are paused";
      state = "paused";
    } else if (pending === active) {
      text = "Waiting for the first checks";
      state = "pending";
    } else {
      text = "All systems operational";
      state = "up";
    }
    h.querySelector(".dot").className = "dot dot-" + state;
    if (h.lastChild.nodeValue !== text) {
      h.lastChild.nodeValue = text;
      flash(h);
    }
  }

  // --- Connection ---

  var source = null;
  var connected = false;
  var delay = 5000;

  function reloadWhenVisible() {
    if (document.visibilityState === "visible") {
      location.reload();
      return;
    }
    document.addEventListener("visibilitychange", function once() {
      document.removeEventListener("visibilitychange", once);
      location.reload();
    });
  }

  function connect() {
    source = new EventSource("/events");
    source.addEventListener("open", function () {
      delay = 5000;
      // A reconnect means events were missed. Load the page again.
      if (connected) reloadWhenVisible();
      connected = true;
    });
    source.addEventListener("check", function (e) { onCheck(JSON.parse(e.data)); });
    source.addEventListener("state", function (e) { onState(JSON.parse(e.data)); });
    source.addEventListener("error", function () {
      // The browser retries a dropped connection by itself. It gives up
      // after an error response, so retry with a growing delay.
      if (source.readyState !== EventSource.CLOSED) return;
      source = null;
      setTimeout(connect, delay);
      delay = Math.min(delay * 2, 60000);
    });
  }
  connect();
})();
