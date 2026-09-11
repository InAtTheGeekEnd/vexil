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

  // Monitor form: switch the fields with the type and fill the name from
  // the target until the user types a name.
  var form = document.getElementById("monitor-form");
  if (form) {
    var name = form.querySelector("[name=name]");
    var typed = name.value !== "";
    form.addEventListener("change", function (e) {
      if (e.target.name === "type") {
        form.dataset.type = e.target.value;
        var first = form.querySelector(".for-" + e.target.value + " input");
        if (first) first.focus();
      }
    });
    name.addEventListener("input", function () { typed = name.value !== ""; });
    form.addEventListener("input", function (e) {
      if (typed || !e.target.hasAttribute("data-target")) return;
      name.value = hostOf(e.target.value);
    });
  }

  function hostOf(v) {
    v = v.trim();
    var m = v.match(/^[a-z][a-z0-9+.-]*:\/\/([^\/?#]+)/i);
    if (m) v = m[1];
    v = v.replace(/^[^@]*@/, "").replace(/:\d+$/, "");
    return v.replace(/^\[|\]$/g, "");
  }

  // Dashboard: drag a row by its grip, or focus the grip and press the
  // arrow keys. Pointer Events cover mouse, pen and touch.
  var list = document.getElementById("monitors");
  if (!list) return;
  var drag = null; // { row, handle, moved }

  list.addEventListener("pointerdown", function (e) {
    var handle = e.target.closest(".mon-handle");
    if (!handle || (e.pointerType === "mouse" && e.button !== 0)) return;
    e.preventDefault();
    drag = { row: handle.closest(".mon-row"), handle: handle, moved: false };
    handle.setPointerCapture(e.pointerId);
    drag.row.classList.add("dragging");
    list.classList.add("dragging");
  });
  list.addEventListener("pointermove", function (e) {
    if (!drag) return;
    var rows = list.querySelectorAll(".mon-row");
    var target = null;
    for (var i = 0; i < rows.length; i++) {
      var box = rows[i].getBoundingClientRect();
      if (e.clientY < box.top + box.height / 2) { target = rows[i]; break; }
    }
    if (target === drag.row || (target === null && drag.row === list.lastElementChild)) return;
    if (target === drag.row.nextElementSibling) return;
    list.insertBefore(drag.row, target);
    drag.moved = true;
  });
  function endDrag() {
    if (!drag) return;
    drag.row.classList.remove("dragging");
    list.classList.remove("dragging");
    if (drag.moved) save();
    drag = null;
  }
  list.addEventListener("pointerup", endDrag);
  list.addEventListener("pointercancel", endDrag);

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
  list.addEventListener("click", barClick);
  list.addEventListener("auxclick", barClick);

  list.addEventListener("keydown", function (e) {
    var handle = e.target.closest(".mon-handle");
    if (!handle) return;
    var row = handle.closest(".mon-row");
    if (e.key === "ArrowUp" && row.previousElementSibling) {
      list.insertBefore(row, row.previousElementSibling);
    } else if (e.key === "ArrowDown" && row.nextElementSibling) {
      list.insertBefore(row.nextElementSibling, row);
    } else {
      return;
    }
    e.preventDefault();
    handle.focus();
    save();
  });

  function save() {
    var body = new URLSearchParams();
    list.querySelectorAll(".mon-row").forEach(function (row) { body.append("id", row.dataset.id); });
    fetch("/monitors/reorder", { method: "POST", body: body, credentials: "same-origin" }).catch(function () {});
  }
})();
