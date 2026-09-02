// Applies the saved appearance before the first paint.
//
// A file of its own rather than an inline script, because the console's CSP
// allows scripts from this origin only. It runs before the bundle so a person
// who chose dark on a light system does not see the light theme flash first.
// "system" -- the default, and what everyone had before there was a choice --
// leaves the decision to the operating system, exactly as before.
(function () {
  var KEY = "mcpd.theme";
  var choice = null;
  try { choice = localStorage.getItem(KEY); } catch (e) { /* private mode */ }
  var dark = choice === "dark" ||
    (choice !== "light" && window.matchMedia("(prefers-color-scheme: dark)").matches);
  document.documentElement.dataset.theme = dark ? "dark" : "light";
})();
