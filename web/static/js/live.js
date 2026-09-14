// Live updates over one SSE connection to /events. Pages that want it
// carry data-live on <body>. The dashboard rows, the detail page, the tab
// title and the favicon update in place. No framework.
(function () {
  "use strict";
  var body = document.body;
  if (!body.hasAttribute("data-live") || !window.EventSource) return;

  var reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  var dash = document.getElementById("dash");
  var lastLine = document.getElementById("last-line");
  var baseTitle = document.title.replace(/^\(\d+\) /, "");
  // The server links the tab icon for the state at load and names the
  // icons for both states.
  var icon = document.querySelector("link[rel=icon][data-down]");
  var down = +body.dataset.down || 0;

  // --- Tab title and favicon ---

  // setDown sets the title and swaps the icon when the page turns up or
  // down. The fragment makes the URL new on every swap: Safari fetches only
  // an icon URL it has not cached.
  function setDown(n) {
    if (icon && (n > 0) !== (down > 0)) {
      icon.setAttribute("href", icon.getAttribute(n > 0 ? "data-down" : "data-up") + "#" + Date.now());
    }
    down = n;
    document.title = (n > 0 ? "(" + n + ") " : "") + baseTitle;
  }

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
    if (dash) el = dash.querySelector('.mon-row[data-id="' + ev.id + '"] .mon-checked');
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
    if (ev.state === "deleted") {
      onDeleted(ev);
      return;
    }
    if (lastLine && +lastLine.dataset.monitor === ev.id) {
      // The tiles, the chart and the incident list all change with the
      // state. A fresh page is simpler than patching each one.
      reloadWhenVisible();
      return;
    }
    if (!dash) return;
    var row = dash.querySelector('.mon-row[data-id="' + ev.id + '"]');
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
    // A DOWN row lifts into the strip above the groups. A recovered row
    // goes back to its place in its group.
    if (ev.state === "down") {
      row.dataset.since = ev.since || 0;
      toStrip(row);
    } else if (old === "down") {
      delete row.dataset.since;
      goHome(row);
    }
    slide(before);
    headline();
  }

  // onDeleted takes a deleted monitor off the page. The detail page of that
  // monitor goes to the dashboard.
  function onDeleted(ev) {
    if (lastLine && +lastLine.dataset.monitor === ev.id) {
      location.replace("/");
      return;
    }
    if (!dash) return;
    var row = dash.querySelector('.mon-row[data-id="' + ev.id + '"]');
    if (!row) return;
    var before = positions();
    row.remove();
    if (!dash.querySelector(".mon-row")) {
      // The last monitor is gone: the server page shows the empty state.
      reloadWhenVisible();
      return;
    }
    slide(before);
    headline();
  }

  // toStrip puts a DOWN row into the strip by outage start, newest first.
  // Equal starts go in monitor id order. The rule must match sortStrip() in
  // Go, so a reload shows the same order.
  function toStrip(row) {
    var strip = dash.querySelector(".mon-strip");
    var since = +row.dataset.since, id = +row.dataset.id;
    var rows = strip.querySelectorAll(".mon-row");
    var next = null;
    for (var i = 0; i < rows.length; i++) {
      var other = +rows[i].dataset.since || 0;
      if (other < since || (other === since && +rows[i].dataset.id > id)) { next = rows[i]; break; }
    }
    strip.insertBefore(row, next);
  }

  // goHome puts a row into its group before the first row with a higher
  // position. A row whose group is not on the page goes to the monitors in
  // no group.
  function goHome(row) {
    var home = dash.querySelector('.monitors[data-group="' + row.dataset.group + '"]') ||
      dash.querySelector('.monitors[data-group="0"]');
    var pos = +row.dataset.pos;
    var rows = home.querySelectorAll(".mon-row");
    var next = null;
    for (var i = 0; i < rows.length; i++) {
      if (+rows[i].dataset.pos > pos) { next = rows[i]; break; }
    }
    home.insertBefore(row, next);
  }

  // Rows that change state slide to their new position (FLIP). The groups
  // and the list of monitors in no group slide too, because the strip above
  // them grows or shrinks. A row inside a sliding box slides by the rest of
  // its move, so the two animations add up.
  var sliding = ".dash-group, .mon-ungrouped, .mon-row";

  function positions() {
    var m = new Map();
    dash.querySelectorAll(sliding).forEach(function (el) {
      if (el.getClientRects().length) m.set(el, el.getBoundingClientRect().top);
    });
    return m;
  }

  function slide(before) {
    if (reduced || !Element.prototype.animate) return;
    var moved = new Map();
    before.forEach(function (top, el) {
      if (el.getClientRects().length) moved.set(el, top - el.getBoundingClientRect().top);
    });
    moved.forEach(function (d, el) {
      if (el.classList.contains("mon-row")) {
        var box = el.parentNode.closest(".dash-group, .mon-ungrouped");
        if (box && moved.has(box)) d -= moved.get(box);
      }
      if (!d) return;
      el.animate([{ transform: "translateY(" + d + "px)" }, { transform: "none" }], { duration: 150, easing: "ease-out" });
    });
  }

  // headline rebuilds the dashboard sentence. It must match headline() in Go.
  function headline() {
    var h = document.querySelector(".dash-head h1");
    if (!h) return;
    var down = 0, pending = 0, active = 0;
    dash.querySelectorAll(".mon-row").forEach(function (r) {
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

  // --- Stale state ---

  // setStale marks the page while it has no event stream: the session ended
  // or the server is gone, so the data on the page is old. The dots turn
  // gray, their pulse stops and one line says so.
  var staleLine = null;

  function setStale(on) {
    body.classList.toggle("stale", on);
    if (!on) {
      if (staleLine) staleLine.remove();
      staleLine = null;
      return;
    }
    var main = document.querySelector("main");
    if (staleLine || !main) return;
    staleLine = document.createElement("p");
    staleLine.className = "alert stale-line";
    staleLine.setAttribute("role", "status");
    staleLine.appendChild(document.createTextNode("The connection is lost. The data on this page is old."));
    var link = document.createElement("a");
    link.href = "/login";
    link.textContent = "Log in again";
    staleLine.appendChild(link);
    main.insertBefore(staleLine, main.firstChild);
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
      setStale(false);
      // A reconnect means events were missed. Load the page again.
      if (connected) reloadWhenVisible();
      connected = true;
    });
    source.addEventListener("check", function (e) { onCheck(JSON.parse(e.data)); });
    source.addEventListener("state", function (e) { onState(JSON.parse(e.data)); });
    source.addEventListener("error", function () {
      // No events arrive from now on, so the page shows old data.
      setStale(true);
      // The browser retries a dropped connection by itself. It gives up
      // after an error response, such as 401 when the session ended, so
      // retry with a growing delay.
      if (source.readyState !== EventSource.CLOSED) return;
      source = null;
      setTimeout(connect, delay);
      delay = Math.min(delay * 2, 60000);
    });
  }
  connect();
})();
