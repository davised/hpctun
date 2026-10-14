// hpctun site: theme toggle, the tunnel step-through, screenshot fallbacks.
(function () {
  "use strict";
  var root = document.documentElement;

  // ---------- theme ----------
  var themeBtn = document.getElementById("theme-btn");
  function currentTheme() {
    if (root.dataset.theme) return root.dataset.theme;
    return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  }
  if (themeBtn) {
    themeBtn.addEventListener("click", function () {
      var next = currentTheme() === "dark" ? "light" : "dark";
      root.dataset.theme = next;
      try { localStorage.setItem("hpctun-theme", next); } catch (e) {}
    });
  }

  // ---------- screenshots: show a placeholder when the image is missing ----------
  document.querySelectorAll("[data-shot]").forEach(function (fig) {
    var img = fig.querySelector("img");
    if (!img) return;
    function missing() { fig.classList.add("missing"); }
    img.addEventListener("error", missing);
    if (img.complete && img.naturalWidth === 0) missing();
  });

  // ---------- tunnel stepper ----------
  var fig = document.getElementById("stepper");
  if (!fig) return;
  var path = document.getElementById("packet-path");
  var packet = document.getElementById("packet");
  var caption = document.getElementById("step-caption");
  var dots = document.getElementById("step-dots");
  var prev = document.getElementById("step-prev");
  var next = document.getElementById("step-next");
  var play = document.getElementById("step-play");
  var els = fig.querySelectorAll(".step-el");
  var reduce = window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  // Distance along #packet-path (M176 132 V236 H820) for each stop.
  var total = path.getTotalLength();
  var stops = [0, 104, 104 + 367, total, 0];  // browser, ssh, login node, node42, back
  var steps = [
    "<b>1.</b> Your browser asks for <code>localhost:8888</code>, a port on your own laptop.",
    "<b>2.</b> ssh, started by hpctun, is listening on that port. It takes the request and sends it down the encrypted connection it already has open to the login node.",
    "<b>3.</b> The login node is allowed to talk to compute nodes. It unwraps the request and passes it to <code>node42</code>, port 8888.",
    "<b>4.</b> Jupyter on node42 answers.",
    "<b>5.</b> The reply travels back the same way, and the page appears in your browser, still at <code>localhost:8888</code>. Your laptop never needed a route to node42."
  ];

  for (var i = 0; i < steps.length; i++) dots.appendChild(document.createElement("span"));

  var step = 0, pos = 0, anim = null, timer = null;

  function place(d) {
    var p = path.getPointAtLength(d);
    packet.setAttribute("cx", p.x);
    packet.setAttribute("cy", p.y);
    pos = d;
  }

  function moveTo(d, done) {
    if (anim) cancelAnimationFrame(anim);
    if (reduce) { place(d); if (done) done(); return; }
    var from = pos, start = null, dur = Math.min(1400, 300 + Math.abs(d - from) * 1.6);
    function frame(t) {
      if (start === null) start = t;
      var k = Math.min(1, (t - start) / dur);
      var e = k < 0.5 ? 2 * k * k : 1 - Math.pow(-2 * k + 2, 2) / 2;
      place(from + (d - from) * e);
      if (k < 1) anim = requestAnimationFrame(frame); else { anim = null; if (done) done(); }
    }
    anim = requestAnimationFrame(frame);
  }

  function show(n) {
    step = (n + steps.length) % steps.length;
    fig.dataset.step = step;
    caption.innerHTML = steps[step];
    els.forEach(function (el) {
      var on = el.getAttribute("data-on").split(" ").indexOf(String(step)) >= 0;
      el.classList.toggle("on", on);
    });
    Array.prototype.forEach.call(dots.children, function (d, i) { d.classList.toggle("on", i === step); });
    packet.classList.toggle("reply", step >= 3);
    prev.disabled = step === 0;
    moveTo(stops[step]);
  }

  function stopPlay() {
    if (timer) { clearInterval(timer); timer = null; }
    play.textContent = "▶ Play";
  }

  prev.addEventListener("click", function () { stopPlay(); show(step - 1); });
  next.addEventListener("click", function () { stopPlay(); show(step + 1); });
  play.addEventListener("click", function () {
    if (timer) { stopPlay(); return; }
    play.textContent = "❚❚ Pause";
    if (step === steps.length - 1) show(0); else show(step + 1);
    timer = setInterval(function () {
      if (step === steps.length - 1) { stopPlay(); return; }
      show(step + 1);
    }, 3200);
  });

  place(0);
  show(0);
})();
