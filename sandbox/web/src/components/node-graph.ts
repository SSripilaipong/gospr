import type { FlightEvent, SandboxState } from "../api";
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
  private pos = new Map<string, { x: number; y: number }>();

  connectedCallback() {
    const wrap = document.createElement("div");
    wrap.className = "graph-wrap";
    this.svg = document.createElementNS(SVG, "svg") as SVGSVGElement;
    this.svg.setAttribute("class", "graph");
    this.svg.setAttribute("viewBox", `0 0 ${SIZE} ${SIZE}`);
    this.svg.setAttribute("role", "img");
    this.svg.setAttribute("aria-label", "cluster graph");
    wrap.appendChild(this.svg);
    this.appendChild(wrap);
    this.render();
  }

  setData(state: SandboxState, selected: string | null) {
    this.state = state;
    this.selected = selected;
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
    this.svg.replaceChildren();

    // Links first (under nodes).
    const ids = s.nodes.map((n) => n.id);
    for (let i = 0; i < ids.length; i++) {
      for (let j = i + 1; j < ids.length; j++) {
        const a = ids[i];
        const b = ids[j];
        const connected = s.links[linkKey(a, b)] ?? true;
        const pa = this.pos.get(a)!;
        const pb = this.pos.get(b)!;
        const line = document.createElementNS(SVG, "line");
        line.setAttribute("x1", String(pa.x));
        line.setAttribute("y1", String(pa.y));
        line.setAttribute("x2", String(pb.x));
        line.setAttribute("y2", String(pb.y));
        line.setAttribute("class", `link ${connected ? "up" : "down"}`);
        line.style.cursor = "pointer";
        line.addEventListener("click", () =>
          this.dispatchEvent(
            new CustomEvent("toggle-link", {
              detail: { a, b, connected: !connected },
              bubbles: true,
            }),
          ),
        );
        this.svg.appendChild(line);

        if (!connected) {
          const mx = (pa.x + pb.x) / 2;
          const my = (pa.y + pb.y) / 2;
          const t = document.createElementNS(SVG, "text");
          t.setAttribute("x", String(mx));
          t.setAttribute("y", String(my));
          t.setAttribute("class", "node-status");
          t.setAttribute("fill", "var(--warn)");
          t.textContent = "✕ disconnected";
          this.svg.appendChild(t);
        }
      }
    }

    // Nodes.
    for (const node of s.nodes) {
      const p = this.pos.get(node.id)!;
      const g = document.createElementNS(SVG, "g");

      const c = document.createElementNS(SVG, "circle");
      c.setAttribute("cx", String(p.x));
      c.setAttribute("cy", String(p.y));
      c.setAttribute("r", "46");
      let cls = "node-circle";
      if (node.initialized) cls += " init";
      if (node.id === this.selected) cls += " selected";
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

      this.svg.appendChild(g);
    }
  }

  // flight animates a traveling dot for an inflight event; dropped flashes the
  // link. Honors reduced-motion via CSS (the dot is hidden, state poll still shows
  // the effect).
  flight(ev: FlightEvent) {
    if (!ev.from || !ev.to) return;
    const pa = this.pos.get(ev.from);
    const pb = this.pos.get(ev.to);
    if (!pa || !pb) return;

    if (ev.status === "dropped") {
      this.flashLink(ev.from, ev.to);
      return;
    }
    if (ev.status !== "inflight") return;

    const dot = document.createElementNS(SVG, "circle");
    dot.setAttribute("r", "6");
    dot.setAttribute("class", `flight-dot ${ev.kind}`);
    dot.setAttribute("cx", String(pa.x));
    dot.setAttribute("cy", String(pa.y));
    this.svg.appendChild(dot);

    const dur = 600;
    const start = performance.now();
    const step = (now: number) => {
      const t = Math.min((now - start) / dur, 1);
      dot.setAttribute("cx", String(pa.x + (pb.x - pa.x) * t));
      dot.setAttribute("cy", String(pa.y + (pb.y - pa.y) * t));
      if (t < 1) {
        requestAnimationFrame(step);
      } else {
        dot.remove();
      }
    };
    requestAnimationFrame(step);
  }

  private flashLink(a: string, b: string) {
    const pa = this.pos.get(a)!;
    const pb = this.pos.get(b)!;
    const flash = document.createElementNS(SVG, "line");
    flash.setAttribute("x1", String(pa.x));
    flash.setAttribute("y1", String(pa.y));
    flash.setAttribute("x2", String(pb.x));
    flash.setAttribute("y2", String(pb.y));
    flash.setAttribute("stroke", "var(--bad)");
    flash.setAttribute("stroke-width", "3");
    this.svg.appendChild(flash);
    setTimeout(() => flash.remove(), 400);
  }
}

function slotLines(node: { collections: Record<string, Record<string, string>> }): string[] {
  const out: string[] = [];
  for (const [coll, slots] of Object.entries(node.collections)) {
    const entries = Object.entries(slots);
    if (entries.length === 0) {
      out.push(`${coll}: ∅`);
    } else {
      const summary = entries.map(([k, v]) => `${k}=${v}`).join(" ");
      out.push(`${coll}: ${summary}`);
    }
    if (out.length >= 3) break;
  }
  return out;
}

customElements.define("node-graph", NodeGraph);
