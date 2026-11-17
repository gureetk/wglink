// The theme toggle and the install tabs. The theme follows the system until
// the toggle picks one, which is remembered; each page's <head> applies a
// saved choice before the first paint.
(function () {
  var root = document.documentElement;
  var system = window.matchMedia("(prefers-color-scheme: dark)");
  var toggles = document.querySelectorAll(".theme-toggle");

  function dark() {
    return root.dataset.theme ? root.dataset.theme === "dark" : system.matches;
  }
  function label() {
    var text = dark() ? "Switch to light mode" : "Switch to dark mode";
    toggles.forEach(function (b) {
      b.setAttribute("aria-label", text);
      b.title = text;
    });
  }
  toggles.forEach(function (b) {
    b.addEventListener("click", function () {
      root.dataset.theme = dark() ? "light" : "dark";
      try {
        localStorage.setItem("theme", root.dataset.theme);
      } catch (e) {}
      label();
    });
  });
  system.addEventListener("change", label);
  label();

  // Tabs: without this script every panel shows, which works too.
  document.querySelectorAll("[role=tablist]").forEach(function (list) {
    var tabs = list.querySelectorAll("[role=tab]");
    function pick(tab) {
      tabs.forEach(function (t) {
        var on = t === tab;
        t.setAttribute("aria-selected", String(on));
        t.tabIndex = on ? 0 : -1;
        document.getElementById(t.getAttribute("aria-controls")).hidden = !on;
      });
    }
    tabs.forEach(function (tab, i) {
      tab.addEventListener("click", function () { pick(tab); });
      tab.addEventListener("keydown", function (e) {
        var step = { ArrowRight: 1, ArrowLeft: -1 }[e.key];
        if (!step) return;
        var next = tabs[(i + step + tabs.length) % tabs.length];
        pick(next);
        next.focus();
      });
    });
    pick(tabs[0]);
  });
})();
