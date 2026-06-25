import type { SandboxState } from "../api";
import { linkKey } from "../util";

const SVG = "http://www.w3.org/2000/svg";
const SIZE = 720;
const CX = SIZE / 2;
const CY = SIZE / 2;
const R = 250; // orbit radius
const NODE_R = 40; // node circle radius
const CHIP_H = 22;
const CHIP_GAP = 6;
const MAX_CHARS = 16;

// <node-graph> draws the cluster as nodes on a circle with links between every pair
// (solid = connected, dashed + muted + label = disconnected). It is the page's focal
// point. Each node shows the SELECTED collection's gossip values as a readable chip
// below the circle, plus a live query-result chip on every node when a label is on.
// A node briefly blinks when its (selected-collection) state changes between polls.
export class NodeGraph extends HTMLElement {
  private state: SandboxState | null = null;
  private selected: string | null = null;
  private collection: string | null = null;
  private liveName: string | null = null;
  private liveResults: Record<string, string> = {};
  private svg!: SVGSVGElement;
  private base!: SVGGElement;
  private pos = new Map<string, { x: number; y: number }>();
  private lastSig = "";
  // Last-seen slot signature per node (selected collection only) to blink on change.
  private prevSlots = new Map<string, string>();

  connectedCallback() {
    const wrap = document.createElement("div");
    wrap.className = "graph-wrap";
    this.svg = document.createElementNS(SVG, "svg") as SVGSVGElement;
    this.svg.setAttribute("class", "graph");
    this.svg.setAttribute("viewBox", `0 0 ${SIZE} ${SIZE}`);
    // role="group" (not "img") so the per-link button controls inside are exposed to
    // screen readers — role="img" would present the SVG as one opaque image.
    this.svg.setAttribute("role", "group");
    this.svg.setAttribute("aria-label", "cluster graph");
    this.base = document.createElementNS(SVG, "g") as SVGGElement;
    this.svg.appendChild(this.base);
    this.appendChild(wrap);
    wrap.appendChild(this.svg);
    this.render();
  }

  setData(
    state: SandboxState,
    selected: string | null,
    collection: string | null,
    liveName: string | null,
    liveResults: Record<string, string>,
  ) {
    this.state = state;
    this.selected = selected;
    this.collection = collection;
    this.liveName = liveName;
    this.liveResults = liveResults;
    // Signature is scoped to the SELECTED collection's slots (+ the live results),
    // NOT all collections — gossip on a hidden collection must not churn the graph.
    const sig = JSON.stringify({
      nodes: state.nodes.map((n) => ({
        id: n.id,
        initialized: n.initialized,
        slots: collection ? (n.collections[collection] ?? null) : null,
      })),
      selected,
      collection,
      liveName,
      liveResults,
      links: state.links,
    });
    if (sig === this.lastSig) return;
    this.lastSig = sig;
    this.computePositions();
    this.render();
  }

  private computePositions() {
    this.pos.clear();
    const ids = this.state?.nodes.map((n) => n.id) ?? [];
    const n = ids.length;
    ids.forEach((id, i) => {
      const angle = -Math.PI / 2 + (2 * Math.PI * i) / Math.max(n, 1);
      this.pos.set(id, { x: CX + R * Math.cos(angle), y: CY + R * Math.sin(angle) });
    });
  }

  private render() {
    if (!this.state) return;
    const s = this.state;
    this.base.replaceChildren();

    // Links first (under nodes).
    const ids = s.nodes.map((n) => n.id);
    for (let i = 0; i < ids.length; i++) {
      for (let j = i + 1; j < ids.length; j++) {
        this.renderLink(ids[i], ids[j], s.links[linkKey(ids[i], ids[j])] ?? true);
      }
    }

    // Nodes.
    for (const node of s.nodes) {
      const p = this.pos.get(node.id)!;
      const g = document.createElementNS(SVG, "g");
      // Attach the group up front so chip() can measure text via getBBox — a detached
      // subtree is outside the render tree and reports a zero-width box (Chrome), which
      // would shrink the chip background to a tiny pill.
      this.base.appendChild(g);

      // Blink when the SELECTED collection's slots changed since the last poll (not
      // on first sighting, and not when clearing to empty on Reset).
      const slots = this.collection ? node.collections[this.collection] : undefined;
      const slotSig = JSON.stringify(slots ?? null);
      const prev = this.prevSlots.get(node.id);
      const changed =
        prev !== undefined && prev !== slotSig && slotSig !== "null" && slotSig !== "{}";
      this.prevSlots.set(node.id, slotSig);

      const c = document.createElementNS(SVG, "circle");
      c.setAttribute("cx", String(p.x));
      c.setAttribute("cy", String(p.y));
      c.setAttribute("r", String(NODE_R));
      let cls = "node-circle";
      if (node.initialized) cls += " init";
      if (node.id === this.selected) cls += " selected";
      if (changed) cls += " blink";
      c.setAttribute("class", cls);
      c.addEventListener("click", () =>
        this.dispatchEvent(
          new CustomEvent("select-node", { detail: { id: node.id }, bubbles: true }),
        ),
      );
      g.appendChild(c);

      // Identity + status inside the circle.
      this.text(g, p.x, p.y - 2, node.id, "node-label");
      this.text(
        g,
        p.x,
        p.y + 14,
        node.initialized ? "● ready" : "○ empty",
        node.initialized ? "node-status good" : "node-status muted",
      );

      // Value chip (raw gossip slots for the selected collection) below the circle.
      let top = p.y + NODE_R + 8;
      const valText = this.collection ? slotChipText(slots) : "";
      if (valText) {
        this.chip(g, p.x, top, valText, "chip-value");
        top += CHIP_H + CHIP_GAP;
      }

      // Live query chip (only when a label is active and this node answered).
      if (this.liveName && node.id in this.liveResults) {
        const txt = clip(`${this.liveName}: ${this.liveResults[node.id]}`);
        this.chip(g, p.x, top, txt, "chip-query");
      }
    }
  }

  private renderLink(a: string, b: string, connected: boolean) {
    const pa = this.pos.get(a)!;
    const pb = this.pos.get(b)!;

    // Wide transparent, keyboard-operable hit target for partitioning. Appended
    // BEFORE the visible line so `.link-hit + .link` hover/focus CSS works and the
    // visible (pointer-events:none) line paints on top.
    const toggle = () =>
      this.dispatchEvent(
        new CustomEvent("toggle-link", { detail: { a, b, connected: !connected }, bubbles: true }),
      );
    const hit = document.createElementNS(SVG, "line");
    setLineCoords(hit, pa, pb);
    hit.setAttribute("class", "link-hit");
    hit.setAttribute("tabindex", "0");
    hit.setAttribute("role", "button");
    hit.setAttribute(
      "aria-label",
      `link ${a} ↔ ${b}, ${connected ? "connected" : "disconnected"} — activate to ${connected ? "disconnect" : "reconnect"}`,
    );
    hit.addEventListener("click", toggle);
    hit.addEventListener("keydown", (e) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        toggle();
      }
    });
    this.base.appendChild(hit);

    const line = document.createElementNS(SVG, "line");
    setLineCoords(line, pa, pb);
    line.setAttribute("class", `link ${connected ? "up" : "down"}`);
    this.base.appendChild(line);

    if (!connected) {
      this.text(this.base, (pa.x + pb.x) / 2, (pa.y + pb.y) / 2, "✕ disconnected", "link-label");
    }
  }

  private text(parent: Element, x: number, y: number, content: string, cls: string) {
    const t = document.createElementNS(SVG, "text");
    t.setAttribute("x", String(x));
    t.setAttribute("y", String(y));
    t.setAttribute("class", cls);
    t.textContent = content;
    parent.appendChild(t);
    return t;
  }

  // chip draws a single-line pill (background rect sized to the text via getBBox)
  // centered horizontally on cx with its top edge at `top`.
  private chip(parent: Element, cx: number, top: number, content: string, cls: string) {
    const t = this.text(parent, cx, top + 15, content, `chip-text ${cls}`);
    const w = t.getBBox().width + 16;
    const rect = document.createElementNS(SVG, "rect");
    rect.setAttribute("x", String(cx - w / 2));
    rect.setAttribute("y", String(top));
    rect.setAttribute("width", String(w));
    rect.setAttribute("height", String(CHIP_H));
    rect.setAttribute("rx", "6");
    rect.setAttribute("class", `chip-bg ${cls}`);
    parent.insertBefore(rect, t);
  }
}

function setLineCoords(
  line: SVGLineElement,
  pa: { x: number; y: number },
  pb: { x: number; y: number },
) {
  line.setAttribute("x1", String(pa.x));
  line.setAttribute("y1", String(pa.y));
  line.setAttribute("x2", String(pb.x));
  line.setAttribute("y2", String(pb.y));
}

// slotChipText renders a collection's slots as bare bracketed values (no nodeID
// keys — user preference): `[5 15 5]`, or `∅` when empty/absent.
function slotChipText(slots: Record<string, string> | undefined): string {
  if (!slots) return "∅";
  const vals = Object.values(slots);
  if (vals.length === 0) return "∅";
  return clip(`[${vals.join(" ")}]`);
}

function clip(s: string): string {
  return s.length > MAX_CHARS ? s.slice(0, MAX_CHARS - 1) + "…" : s;
}

customElements.define("node-graph", NodeGraph);
