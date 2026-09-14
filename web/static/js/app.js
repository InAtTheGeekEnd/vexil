// Small page behaviors: confirm dialogs, copy buttons, the monitor form and
// drag to reorder on the dashboard. No framework.
(function () {
  "use strict";

  // Forms with data-confirm ask before they submit.
  document.addEventListener("submit", function (e) {
    var form = e.target;
    if (form.dataset && form.dataset.confirm && !window.confirm(form.dataset.confirm)) {
      e.preventDefault();
    }
  });

  // Buttons with data-copy put their value on the clipboard.
  document.addEventListener("click", function (e) {
    var btn = e.target.closest("[data-copy]");
    if (!btn || !navigator.clipboard) return;
    navigator.clipboard.writeText(btn.dataset.copy).then(function () {
      var label = btn.textContent;
      btn.textContent = "Copied";
      setTimeout(function () { btn.textContent = label; }, 1500);
    });
  });

  // Forms with type cards show only the fields of the chosen type.
  var form = document.querySelector("form[data-type]");
  if (form) {
    form.addEventListener("change", function (e) {
      if (e.target.name === "type") {
        form.dataset.type = e.target.value;
        var first = form.querySelector(".for-" + e.target.value + " input:not([hidden])");
        if (first) first.focus();
      }
    });
  }

  // Monitor form: fill the name from the target until the user types a name.
  if (form && form.id === "monitor-form") {
    var name = form.querySelector("[name=name]");
    var typed = name.value !== "";
    name.addEventListener("input", function () { typed = name.value !== ""; });
    form.addEventListener("input", function (e) {
      if (typed || !e.target.hasAttribute("data-target")) return;
      name.value = hostOf(e.target.value);
    });
  }

  // Channel form: a saved secret shows as dots until the user clicks Replace.
  document.addEventListener("click", function (e) {
    var btn = e.target.closest("[data-replace]");
    if (!btn) return;
    var wrap = btn.closest(".secret");
    var input = wrap.nextElementSibling;
    wrap.hidden = true;
    input.hidden = false;
    input.focus();
  });

  function hostOf(v) {
    v = v.trim();
    var m = v.match(/^[a-z][a-z0-9+.-]*:\/\/([^\/?#]+)/i);
    if (m) v = m[1];
    v = v.replace(/^[^@]*@/, "").replace(/:\d+$/, "");
    return v.replace(/^\[|\]$/g, "");
  }

  // Detail page: the response chart range toggle.
  var toggle = document.querySelector(".range-toggle");
  if (toggle) {
    toggle.addEventListener("click", function (e) {
      var btn = e.target.closest("button[data-range]");
      if (!btn) return;
      toggle.querySelectorAll("button").forEach(function (b) { b.setAttribute("aria-pressed", b === btn ? "true" : "false"); });
      document.querySelectorAll(".range[data-range]").forEach(function (r) { r.hidden = r.dataset.range !== btn.dataset.range; });
    });
  }

  // Dashboard: drag a monitor row or a group heading by its grip, or focus
  // the grip and press the arrow keys. Pointer Events cover mouse, pen and
  // touch. A row moves inside its group, into another group, or into the
  // monitors in no group. A DOWN row waits in the strip and cannot move.
  var dash = document.getElementById("dash");
  if (!dash) return;
  var groups = document.getElementById("groups"); // null without groups
  var drag = null; // { item, kind, pointer, y, moved }; kind is "row" or "group"

  // lists returns the lists a row can go into, in page order: the groups,
  // then the monitors in no group. The strip is not one of them.
  function lists() {
    return Array.prototype.slice.call(dash.querySelectorAll(".monitors[data-group]"));
  }

  dash.addEventListener("pointerdown", function (e) {
    var handle = e.target.closest(".mon-handle, .group-handle");
    if (!handle || (e.pointerType === "mouse" && e.button !== 0)) return;
    var kind = handle.classList.contains("group-handle") ? "group" : "row";
    var item = handle.closest(kind === "group" ? ".dash-group" : ".mon-row");
    if (item.closest(".mon-strip")) return;
    e.preventDefault();
    drag = { item: item, kind: kind, pointer: e.pointerId, y: e.clientY, moved: false };
    handle.setPointerCapture(e.pointerId);
    item.classList.add("dragging");
    dash.dataset.drag = kind;
  });
  // The move and end listeners sit on the document. A row that moves into
  // another list can lose the pointer capture, and the pointer can leave
  // the dashboard before the drop.
  document.addEventListener("pointermove", function (e) {
    if (!drag || e.pointerId !== drag.pointer) return;
    if (drag.kind === "group") {
      dragGroup(e.clientY);
    } else if (drag.item.closest(".mon-strip")) {
      endDrag(); // the monitor went down during the drag
    } else {
      dragRow(e.clientY);
    }
  });
  function endDrag(e) {
    if (!drag || (e && e.pointerId !== drag.pointer)) return;
    drag.item.classList.remove("dragging");
    delete dash.dataset.drag;
    if (drag.moved) save();
    drag = null;
  }
  document.addEventListener("pointerup", endDrag);
  document.addEventListener("pointercancel", endDrag);

  // dragGroup moves the dragged group past the group under the pointer:
  // after it when the pointer moves down, before it when the pointer moves
  // up. Every part of a group is a target, the heading and the rows. The
  // direction keeps a group from jumping back while the pointer is still
  // over the group it just passed.
  function dragGroup(y) {
    if (Math.abs(y - drag.y) < 4) return;
    var down = y > drag.y;
    drag.y = y;
    var others = groups.querySelectorAll(".dash-group");
    for (var i = 0; i < others.length; i++) {
      var box = others[i].getBoundingClientRect();
      if (others[i] === drag.item || y < box.top || y > box.bottom) continue;
      var below = drag.item.compareDocumentPosition(others[i]) & Node.DOCUMENT_POSITION_FOLLOWING;
      if (down && below) move(groups, drag.item, others[i].nextElementSibling);
      if (!down && !below) move(groups, drag.item, others[i]);
      return;
    }
  }

  // dragRow puts the dragged row in the last list that starts above the
  // pointer. The list of a group starts at its heading, so a row can drop
  // into an empty group. In the list, the row goes before the first other
  // row whose middle is below the pointer.
  function dragRow(y) {
    var all = lists();
    var list = all[0];
    all.forEach(function (l) {
      if ((l.closest(".dash-group") || l).getBoundingClientRect().top <= y) list = l;
    });
    var rows = list.querySelectorAll(".mon-row");
    var target = null;
    for (var i = 0; i < rows.length; i++) {
      if (rows[i] === drag.item) continue;
      var box = rows[i].getBoundingClientRect();
      if (y < box.top + box.height / 2) { target = rows[i]; break; }
    }
    move(list, drag.item, target);
  }

  function move(parent, item, before) {
    if (item.parentNode === parent && item.nextElementSibling === before) return;
    parent.insertBefore(item, before);
    drag.moved = true;
  }

  // A click on the uptime bar, which sits above the stretched link, still
  // opens the monitor. Cmd, Ctrl, Shift and middle clicks open a new tab
  // or window, like a real link.
  function barClick(e) {
    var bar = e.target.closest(".mon-uptime");
    if (!bar || (e.button !== 0 && e.button !== 1)) return;
    var link = bar.closest(".mon-row").querySelector(".mon-link");
    if (!link) return;
    e.preventDefault();
    if (e.button === 1 || e.metaKey || e.ctrlKey || e.shiftKey) {
      window.open(link.href, "_blank");
    } else {
      link.click();
    }
  }
  dash.addEventListener("click", barClick);
  dash.addEventListener("auxclick", barClick);

  dash.addEventListener("keydown", function (e) {
    if (e.key !== "ArrowUp" && e.key !== "ArrowDown") return;
    var handle = e.target.closest(".mon-handle, .group-handle");
    if (!handle) return;
    var up = e.key === "ArrowUp";
    var moved = handle.classList.contains("group-handle")
      ? stepGroup(handle.closest(".dash-group"), up)
      : stepRow(handle.closest(".mon-row"), up);
    if (!moved) return;
    e.preventDefault();
    handle.focus();
    save();
  });

  // stepGroup moves a group one place up or down.
  function stepGroup(group, up) {
    var other = up ? group.previousElementSibling : group.nextElementSibling;
    if (!other) return false;
    groups.insertBefore(up ? group : other, up ? other : group);
    return true;
  }

  // stepRow moves a row one place up or down. At the end of its list it
  // moves into the next list, so the keys reach every group.
  function stepRow(row, up) {
    if (row.closest(".mon-strip")) return false;
    var list = row.parentNode;
    var other = up ? row.previousElementSibling : row.nextElementSibling;
    if (other) {
      list.insertBefore(up ? row : other, up ? other : row);
      return true;
    }
    var all = lists();
    var next = all[all.indexOf(list) + (up ? -1 : 1)];
    if (!next) return false;
    next.insertBefore(row, up ? null : next.firstElementChild);
    return true;
  }

  // save posts the whole layout. It numbers the rows like the server does:
  // from 1 in each group, skipping the positions that the DOWN rows of that
  // group keep. live.js puts a recovered row back by these numbers.
  function save() {
    var body = new URLSearchParams();
    dash.querySelectorAll(".dash-group").forEach(function (g) { body.append("group", g.dataset.group); });
    lists().forEach(function (list) {
      var group = list.dataset.group;
      var kept = {};
      dash.querySelectorAll('.mon-strip .mon-row[data-group="' + group + '"]').forEach(function (r) { kept[r.dataset.pos] = true; });
      var pos = 0;
      list.querySelectorAll(".mon-row").forEach(function (row) {
        do { pos++; } while (kept[pos]);
        row.dataset.pos = pos;
        row.dataset.group = group;
        body.append("id", row.dataset.id);
        body.append("in", group);
      });
    });
    fetch("/monitors/reorder", { method: "POST", body: body, credentials: "same-origin" }).catch(function () {});
  }
})();
