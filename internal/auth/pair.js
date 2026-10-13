// Pairing links carry the code in the URL fragment so it never reaches a server
// log. Copy it into the form, then drop it from the address bar and history.
(function () {
  var raw = location.hash.slice(1);
  if (!raw) return;
  var code = raw;
  try {
    code = decodeURIComponent(raw);
  } catch (e) {
    code = raw;
  }
  var input = document.getElementById("code");
  if (input) input.value = code.slice(0, 9);
  history.replaceState(null, "", "/pair");
})();
