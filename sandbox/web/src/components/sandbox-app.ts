import { getState, openEvents, setLink, type FlightEvent, type SandboxState } from "../api";
import { toast } from "../toast";
import { NodeGraph } from "./node-graph";
import { NodePanel } from "./node-panel";
import { NetworkControls } from "./network-controls";

const POLL_MS = 750;

// <sandbox-app> is the orchestrator: it polls /state, streams /events to animate
// the graph, owns the selected node, and wires child component events.
export class SandboxApp extends HTMLElement {
  private graph!: NodeGraph;
  private panel!: NodePanel;
  private controls!: NetworkControls;
  private selected: string | null = null;
  private state: SandboxState | null = null;
  private pollTimer?: number;
  private es?: EventSource;

  connectedCallback() {
    this.innerHTML = `
      <div class="app">
        <header>
          <h1>gospr sandbox</h1>
          <span class="sub">watch gossip propagate · partition links · deploy &amp; query</span>
        </header>
        <div class="left"></div>
        <div class="right stack"></div>
      </div>`;

    const left = this.querySelector(".left")!;
    const right = this.querySelector(".right")!;

    this.graph = new NodeGraph();
    this.panel = new NodePanel();
    this.controls = new NetworkControls();
    left.appendChild(this.graph);
    right.appendChild(this.panel);
    right.appendChild(this.controls);

    this.addEventListener("select-node", (e) => {
      const id = (e as CustomEvent).detail.id as string;
      this.selected = this.selected === id ? null : id;
      this.pushData();
    });
    this.addEventListener("toggle-link", async (e) => {
      const { a, b, connected } = (e as CustomEvent).detail;
      try {
        await setLink(a, b, connected);
        await this.poll();
      } catch (err) {
        toast(String((err as Error).message), "error");
      }
    });
    this.addEventListener("refresh", () => void this.poll());

    void this.poll();
    this.pollTimer = window.setInterval(() => void this.poll(), POLL_MS);

    this.es = openEvents((ev) => this.onEvent(ev));
  }

  disconnectedCallback() {
    if (this.pollTimer) window.clearInterval(this.pollTimer);
    this.es?.close();
  }

  private onEvent(ev: FlightEvent) {
    // Messages are no longer animated; we only react to Reset (state changes are
    // shown by the node blink driven from the /state poll).
    if (ev.kind === "reset") {
      this.selected = null;
      void this.poll();
    }
  }

  private async poll() {
    try {
      const next = await getState();
      this.state = next;
      // Drop a stale selection (e.g. after Reset changed the topology).
      if (this.selected && !next.nodes.some((n) => n.id === this.selected)) {
        this.selected = null;
      }
      this.pushData();
    } catch {
      /* transient; next tick retries */
    }
  }

  private pushData() {
    if (!this.state) return;
    this.graph.setData(this.state, this.selected);
    this.panel.setData(this.state, this.selected);
    this.controls.setData(this.state);
  }
}

customElements.define("sandbox-app", SandboxApp);
