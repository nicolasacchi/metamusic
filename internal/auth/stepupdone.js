// Second-tab step-up (D1): tells the tab that opened this one that the password was confirmed, then closes.
// The signal is only a hint; the opener retries its request and the server gates it on its own.
(function () {
  try {
    var c = new BroadcastChannel("mm-stepup");
    c.postMessage({ type: "done" });
    setTimeout(function () {
      c.close();
      window.close();
    }, 100);
  } catch (e) {
    /* no BroadcastChannel: the opener falls back to its visibilitychange re-check */
  }
})();
