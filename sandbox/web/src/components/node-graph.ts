import type { SandboxState } from "../api";
import { linkKey } from "../util";

const SVG = "http://www.w3.org/2000/svg";
const SIZE = 640;
const CX = SIZE / 2;
const CY = SIZE / 2;
const R = 230;

// <node-graph> draws the cluster as nodes on a circle with links between every
// pair (solid = connected, dashed + muted + label = disconnected). Gossip/deploy
// events animate a dot traveling from -> to. It is the page's focal point.
export class NodeGraph extends HTMLElement {
  private state: SandboxState | null = null;
  private selected: string | null = null;
  private svg!: SVGSVGElement;
  // base holds links + nodes, rebuilt on each render.
  private base!: SVGGElement;
  private pos = new Map<string, { x: number; y: number }>();
  private lastSig = "";
  // Last-seen slot signature per node, to blink a node when its state changes.
  private prevSlots = new Map<string, string>();

  connectedCallback() {
    const wrap = document.createElement("div");
    wrap.className = "graph-wrap";
    this.svg = document.createElementNS(SVG, "svg") as SVGSVGElement;
    this.svg.setAttribute("class", "graph");
    this.svg.setAttribute("viewBox", `0 0 ${SIZE} ${SIZE}`);
    // role="group" (not "img") so the per-link button controls inside are exposed
    // to screen readers — role="img" would present the SVG as one opaque image.
    this.svg.setAttribute("role", "group");
    this.svg.setAttribute("aria-label", "cluster graph");
    this.base = document.createElementNS(SVG, "g") as SVGGElement;
    this.svg.appendChild(this.base);
    this.appendChild(wrap);
    wrap.appendChild(this.svg);
    this.render();
  }

  setData(state: SandboxState, selected: string | null) {
    this.state = state;
    this.selected = selected;
    // The graph reflects gossip (slot values), so collections ARE part of the sig;
    // skip only truly identical polls to avoid redundant rebuilds.
    const sig = JSON.stringify({
      nodes: state.nodes.map((n) => ({
        id: n.id,
        initialized: n.initialized,
        collections: n.collections,
      })),
      selected,
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
      // Start at top, go clockwise.
      const angle = -Math.PI / 2 + (2 * Math.PI * i) / Math.max(n, 1);
      this.pos.set(id, {
        x: CX + R * Math.cos(angle),
        y: CY + R * Math.sin(angle),
      });
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
        const a = ids[i];
        const b = ids[j];
        const connected = s.links[linkKey(a, b)] ?? true;
        const pa = this.pos.get(a)!;
        const pb = this.pos.get(b)!;

        // Wide transparent, keyboard-operable hit target (this replaces the old
        // per-pair buttons, so it must be reachable + activatable by keyboard).
        // Appended BEFORE the visible line so the `.link-hit + .link` hover/focus
        // CSS works and the visible (pointer-events:none) line paints on top.
        const toggle = () =>
          this.dispatchEvent(
            new CustomEvent("toggle-link", {
              detail: { a, b, connected: !connected },
              bubbles: true,
            }),
          );
        const hit = document.createElementNS(SVG, "line");
        hit.setAttribute("x1", String(pa.x));
        hit.setAttribute("y1", String(pa.y));
        hit.setAttribute("x2", String(pb.x));
        hit.setAttribute("y2", String(pb.y));
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

        // Visible styling line — non-interactive so clicks land on the hit-line.
        const line = document.createElementNS(SVG, "line");
        line.setAttribute("x1", String(pa.x));
        line.setAttribute("y1", String(pa.y));
        line.setAttribute("x2", String(pb.x));
        line.setAttribute("y2", String(pb.y));
        line.setAttribute("class", `link ${connected ? "up" : "down"}`);
        this.base.appendChild(line);

        if (!connected) {
          const mx = (pa.x + pb.x) / 2;
          const my = (pa.y + pb.y) / 2;
          const t = document.createElementNS(SVG, "text");
          t.setAttribute("x", String(mx));
          t.setAttribute("y", String(my));
          t.setAttribute("class", "node-status");
          t.setAttribute("fill", "var(--warn)");
          t.textContent = "✕ disconnected";
          this.base.appendChild(t);
        }
      }
    }

    // Nodes.
    for (const node of s.nodes) {
      const p = this.pos.get(node.id)!;
      const g = document.createElementNS(SVG, "g");

      // Blink the node when its slot state actually changed since the last poll
      // (but not when clearing to empty on Reset, and not on first sighting).
      const slotSig = JSON.stringify(node.collections);
      const prev = this.prevSlots.get(node.id);
      const changed = prev !== undefined && prev !== slotSig && slotSig !== "{}";
      this.prevSlots.set(node.id, slotSig);

      const c = document.createElementNS(SVG, "circle");
      c.setAttribute("cx", String(p.x));
      c.setAttribute("cy", String(p.y));
      c.setAttribute("r", "46");
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

      const label = document.createElementNS(SVG, "text");
      label.setAttribute("x", String(p.x));
      label.setAttribute("y", String(p.y - 24));
      label.setAttribute("class", "node-label");
      label.textContent = node.id;
      g.appendChild(label);

      const status = document.createElementNS(SVG, "text");
      status.setAttribute("x", String(p.x));
      status.setAttribute("y", String(p.y - 8));
      status.setAttribute("class", "node-status");
      status.setAttribute("fill", node.initialized ? "var(--good)" : "var(--text-muted)");
      status.textContent = node.initialized ? "● ready" : "○ empty";
      g.appendChild(status);

      // Slot values: collection slots, a couple of lines.
      const lines = slotLines(node);
      lines.forEach((ln, k) => {
        const t = document.createElementNS(SVG, "text");
        t.setAttribute("x", String(p.x));
        t.setAttribute("y", String(p.y + 10 + k * 13));
        t.setAttribute("class", "node-slot");
        t.textContent = ln;
        g.appendChild(t);
      });

      this.base.appendChild(g);
    }
  }

}

function slotLines(node: { collections: Record<string, Record<string, string>> }): string[] {
  const out: string[] = [];
  for (const [coll, slots] of Object.entries(node.collections)) {
    const entries = Object.entries(slots);
    if (entries.length === 0) {
      out.push(`${coll}: ∅`);
    } else {
      const summary = entries.map(([, v]) => v).join(" ");
      out.push(`${coll}: [${summary}]`);
    }
    if (out.length >= 3) break;
  }
  return out;
}

customElements.define("node-graph", NodeGraph);
