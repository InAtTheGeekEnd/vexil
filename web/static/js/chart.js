// Hover crosshair and value tooltip for server-rendered response charts.
// The SVG carries data-points="x:y:label|x:y:label|..." in viewBox units.
(function () {
  function setup(svg) {
    var raw = svg.getAttribute("data-points");
    if (!raw) return;
    var points = raw.split("|").map(function (p) {
      var parts = p.split(":");
      return { x: +parts[0], y: +parts[1], label: parts.slice(2).join(":") };
    });
    if (!points.length) return;

    var ns = "http://www.w3.org/2000/svg";
    var vb = svg.viewBox.baseVal;
    var line = document.createElementNS(ns, "line");
    line.setAttribute("class", "chart-crosshair");
    line.setAttribute("y1", "0");
    line.setAttribute("y2", String(vb.height));
    var dot = document.createElementNS(ns, "circle");
    dot.setAttribute("class", "chart-hover-dot");
    dot.setAttribute("r", "3.5");
    svg.appendChild(line);
    svg.appendChild(dot);

    var tip = document.createElement("div");
    tip.className = "chart-tip";
    tip.hidden = true;
    svg.parentNode.appendChild(tip);

    function hide() {
      line.style.display = "none";
      dot.style.display = "none";
      tip.hidden = true;
    }
    hide();

    svg.addEventListener("mousemove", function (e) {
      var rect = svg.getBoundingClientRect();
      var x = (e.clientX - rect.left) / rect.width * vb.width;
      var best = points[0];
      for (var i = 1; i < points.length; i++) {
        if (Math.abs(points[i].x - x) < Math.abs(best.x - x)) best = points[i];
      }
      line.style.display = "";
      dot.style.display = "";
      line.setAttribute("x1", String(best.x));
      line.setAttribute("x2", String(best.x));
      dot.setAttribute("cx", String(best.x));
      dot.setAttribute("cy", String(best.y));
      tip.textContent = best.label;
      tip.hidden = false;
      var px = best.x / vb.width * rect.width;
      tip.style.left = Math.min(Math.max(px, 40), rect.width - 40) + "px";
    });
    svg.addEventListener("mouseleave", hide);
  }

  document.addEventListener("DOMContentLoaded", function () {
    var charts = document.querySelectorAll("svg.chart[data-points]");
    for (var i = 0; i < charts.length; i++) setup(charts[i]);
  });
})();
