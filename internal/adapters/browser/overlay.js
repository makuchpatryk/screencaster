// Annotation overlay for screenshots scripts (decision 74). A function
// (el, opts) => void, run in the page through Locator.Evaluate, so el is the
// one element the selector matched. It draws opts.dim, opts.box, opts.arrow
// and opts.label around el in a single div#__sc_annot, in document
// coordinates, so a full-page shot shows them in place. Placement is a pure
// function of el's rect and the document size: the same page gives the same
// pixels. Capture removes the div after the shot.
(el, opts) => {
  const COLOR = "#ff2d55";
  const PAD = 6;       // box and arrow tip sit this far outside the element
  const BORDER = 4;
  const ARROW_LEN = 85; // per axis: about 120 px along the diagonal
  const HEAD = 22;
  const SHADOW = "rgba(0,0,0,.55)";

  document.getElementById("__sc_annot")?.remove();

  const docW = Math.max(document.documentElement.scrollWidth, document.body.scrollWidth);
  const r = el.getBoundingClientRect();
  const root = document.createElement("div");
  root.id = "__sc_annot";
  root.style.cssText = "position:absolute;left:0;top:0;width:0;height:0;pointer-events:none;z-index:2147483647";
  document.body.appendChild(root);

  // root sits at its containing block's origin, which is not the document's
  // when body is positioned: measure it once and subtract.
  const o = root.getBoundingClientRect();
  const x = r.left - o.left, y = r.top - o.top, w = r.width, h = r.height;

  const place = (node, css) => {
    node.style.cssText = "position:absolute;box-sizing:border-box;" + css;
    root.appendChild(node);
    return node;
  };
  const px = (n) => Math.round(n) + "px";

  if (opts.dim) {
    place(document.createElement("div"),
      `left:${px(x)};top:${px(y)};width:${px(w)};height:${px(h)};box-shadow:0 0 0 100000px ${SHADOW}`);
  }
  if (opts.box) {
    place(document.createElement("div"),
      `left:${px(x - PAD)};top:${px(y - PAD)};width:${px(w + 2 * PAD)};height:${px(h + 2 * PAD)};border:${BORDER}px solid ${COLOR}`);
  }

  // The arrow points at the top-left corner of the box from up-left, and
  // flips to the right and/or below when the document edge leaves no room.
  let tail = null;
  if (opts.arrow) {
    const tip = { x: x - PAD, y: y - PAD };
    const dx = tip.x - ARROW_LEN - 12 < 0 ? ARROW_LEN : -ARROW_LEN;
    const dy = tip.y - ARROW_LEN - 12 < 0 ? ARROW_LEN : -ARROW_LEN;
    tail = { x: tip.x + dx, y: tip.y + dy };

    const m = 12; // room for the stroke and the head
    const left = Math.min(tail.x, tip.x) - m, top = Math.min(tail.y, tip.y) - m;
    const bw = Math.abs(dx) + 2 * m, bh = Math.abs(dy) + 2 * m;
    const t = { x: tip.x - left, y: tip.y - top }, s = { x: tail.x - left, y: tail.y - top };
    const a = Math.atan2(t.y - s.y, t.x - s.x);
    const c = Math.cos(a), n = Math.sin(a);
    const base = { x: t.x - HEAD * c, y: t.y - HEAD * n }; // where the shaft meets the head
    const f = (v) => v.toFixed(1);
    const head = `${f(t.x)},${f(t.y)} ${f(base.x - 11 * n)},${f(base.y + 11 * c)} ${f(base.x + 11 * n)},${f(base.y - 11 * c)}`;
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("width", bw);
    svg.setAttribute("height", bh);
    svg.innerHTML =
      `<line x1="${f(s.x)}" y1="${f(s.y)}" x2="${f(base.x)}" y2="${f(base.y)}" stroke="#fff" stroke-width="10" stroke-linecap="round"/>` +
      `<polygon points="${head}" fill="#fff" stroke="#fff" stroke-width="6" stroke-linejoin="round"/>` +
      `<line x1="${f(s.x)}" y1="${f(s.y)}" x2="${f(base.x)}" y2="${f(base.y)}" stroke="${COLOR}" stroke-width="6" stroke-linecap="round"/>` +
      `<polygon points="${head}" fill="${COLOR}"/>`;
    place(svg, `left:${px(left)};top:${px(top)};width:${px(bw)};height:${px(bh)}`);
  }

  if (opts.label) {
    const pill = place(document.createElement("div"),
      `left:0;top:0;padding:6px 14px;border-radius:999px;white-space:nowrap;background:${COLOR};color:#fff;font:600 20px/1.2 system-ui,-apple-system,"Segoe UI",sans-serif`);
    pill.textContent = opts.label;
    const pw = pill.offsetWidth, ph = pill.offsetHeight;
    let lx, ly;
    if (tail) { // the tail touches the pill's corner nearest the element
      lx = tail.x < x ? tail.x - pw : tail.x;
      ly = tail.y < y ? tail.y - ph : tail.y;
    } else {    // above the box, or below it with no room above
      lx = x - PAD;
      ly = y - PAD - 8 - ph;
      if (ly < 0) ly = y + h + PAD + 8;
    }
    lx = Math.max(0, Math.min(lx, docW - pw));
    ly = Math.max(0, ly);
    pill.style.left = px(lx);
    pill.style.top = px(ly);
  }
}
