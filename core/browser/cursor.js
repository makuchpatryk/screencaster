// Cursor overlay injected as an init script when visuals are on (FR-006).
// Chromium's headless video has no system cursor, so the page draws one that
// follows the mousemove events Playwright dispatches. It stays hidden until the
// first move, because a new document starts without a known mouse position.
(() => {
  const cursor = document.createElement("div");
  cursor.id = "__screencaster_cursor";
  cursor.style.cssText = [
    "position:fixed", "left:0", "top:0", "width:28px", "height:28px",
    "z-index:2147483647", "pointer-events:none", "display:none",
    "background:url(\"data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' width='28' height='28' viewBox='0 0 28 28'><path d='M3 2l18 11-8 2 5 9-3 1.5-5-9-6 6z' fill='black' stroke='white' stroke-width='1.5' stroke-linejoin='round'/></svg>\") no-repeat",
  ].join(";");
  document.documentElement.appendChild(cursor);
  document.addEventListener("mousemove", (e) => {
    cursor.style.display = "block";
    cursor.style.transform = `translate(${e.clientX}px, ${e.clientY}px)`;
  }, true);
})();
