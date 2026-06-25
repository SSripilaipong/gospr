import {
  getState,
  openEvents,
  queryAll,
  reset,
  setLink,
  type FlightEvent,
  type SandboxState,
} from "../api";
import { toast } from "../toast";
import { NodeGraph } from "./node-graph";
import { NodePanel } from "./node-panel";
import { DeployModal } from "./deploy-modal";

const POLL_MS = 750;

// <sandbox-app> is the app-shell orchestrator: a topbar (collection selector, deploy,
// reset), the focal graph, and a right inspector rail. It polls /state, owns the
// selected node / selected collection / live-query choice, fetches the live query for
// every node when a label is on, and opens the deploy modal on demand.
export class SandboxApp extends HTMLElement {
  private graph!: NodeGraph;
  private panel!: NodePanel;
  private modal!: DeployModal;
  private collSelect!: HTMLSelectElement;
  private deployBtn!: HTMLButtonElement;

  private selected: string | null = null;
  private collection: string | null = null;
  private liveQuery: string | null = null;
  private liveResults: Record<string, string> = {};
  private lastDeployedSource: string | null = null;
  private state: SandboxState | null = null;

  private pollTimer?: number;
  private es?: EventSource;
  private polling = false;
  private topbarSig = "";

  connectedCallback() {
    this.innerHTML = `
      <div class="shell">
        <header class="topbar">
          <div class="brand">
            <h1>gospr sandbox</h1>
            <span class="sub">watch gossip · partition links · deploy &amp; query</span>
          </div>
          <div class="topbar-actions">
            <select class="collection-select" aria-label="Selected collection" hidden></select>
            <button class="primary btn-deploy" type="button">Deploy</button>
            <button class="danger btn-reset" type="button">Reset</button>
          </div>
        </header>
        <main class="canvas"></main>
        <aside class="inspector stack"></aside>
      </div>`;

    this.graph = new NodeGraph();
    this.panel = new NodePanel();
    this.querySelector(".canvas")!.appendChild(this.graph);
    this.querySelector(".inspector")!.appendChild(this.panel);

    // Construct the modal (a value use of DeployModal, so the bundler keeps its
    // customElements.define side effect) and append it as a sibling of .shell — it
    // must live OUTSIDE .shell so the `inert` we set on .shell doesn't disable it.
    this.modal = new DeployModal();
    this.appendChild(this.modal);
    this.collSelect = this.querySelector(".collection-select")!;
    this.deployBtn = this.querySelector(".btn-deploy")!;

    this.collSelect.addEventListener("change", () => {
      this.collection = this.collSelect.value || null;
      this.liveQuery = null; // live-label choice is collection-scoped
      this.liveResults = {};
      void this.poll();
    });
    this.deployBtn.addEventListener("click", () => this.openDeploy());
    this.querySelector(".btn-reset")!.addEventListener("click", () => void this.doReset());

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
    this.addEventListener("set-live-query", (e) => {
      this.liveQuery = (e as CustomEvent).detail.name as string | null;
      this.liveResults = {};
      void this.poll();
    });
    this.addEventListener("refresh", () => void this.poll());
    this.addEventListener("deployed", (e) => {
      this.lastDeployedSource = (e as CustomEvent).detail.code as string;
      void this.poll();
    });

    void this.poll();
    this.pollTimer = window.setInterval(() => void this.poll(), POLL_MS);
    this.es = openEvents((ev) => this.onEvent(ev));
  }

  disconnectedCallback() {
    if (this.pollTimer) window.clearInterval(this.pollTimer);
    this.es?.close();
  }

  private onEvent(ev: FlightEvent) {
    // The SSE stream only carries Reset now (state changes show via the node blink
    // driven from the /state poll).
    if (ev.kind === "reset") {
      this.selected = null;
      this.collection = null;
      this.liveQuery = null;
      this.liveResults = {};
      void this.poll();
    }
  }

  private openDeploy() {
    if (!this.state) return;
    this.modal.open({
      nodes: this.state.nodes.map((n) => n.id),
      target: this.selected ?? this.state.nodes[0]?.id ?? null,
      hasPlan: this.hasPlan(),
      source: this.lastDeployedSource,
    });
  }

  private async doReset() {
    if (!confirm("Reset the cluster? All deployed code and state will be discarded.")) return;
    try {
      await reset();
      this.lastDeployedSource = null;
      this.selected = null;
      this.collection = null;
      this.liveQuery = null;
      this.liveResults = {};
      toast("Cluster reset");
      await this.poll();
    } catch (e) {
      toast(String((e as Error).message), "error");
    }
  }

  private hasPlan(): boolean {
    return !!this.state && Object.keys(this.state.schema).length > 0;
  }

  private async poll() {
    if (this.polling) return; // avoid overlap when a live-query fan-out is slow
    this.polling = true;
    try {
      const next = await getState();
      this.state = next;
      this.reconcile(next);
      await this.refreshLiveResults(next);
      this.pushData();
    } catch {
      /* transient; next tick retries */
    } finally {
      this.polling = false;
    }
  }

  // reconcile keeps the selected node / collection / live-query valid as the cluster
  // changes (deploy, reset, topology change).
  private reconcile(next: SandboxState) {
    if (this.selected && !next.nodes.some((n) => n.id === this.selected)) {
      this.selected = null;
    }
    const colls = Object.keys(next.schema);
    if (this.collection && !colls.includes(this.collection)) this.collection = null;
    if (!this.collection && colls.length > 0) this.collection = colls[0];

    if (this.collection) {
      const queries = next.schema[this.collection]?.queries ?? {};
      if (this.liveQuery && !(this.liveQuery in queries)) this.liveQuery = null;
    } else {
      this.liveQuery = null;
    }
  }

  private async refreshLiveResults(next: SandboxState) {
    if (!this.liveQuery || !this.collection) {
      this.liveResults = {};
      return;
    }
    // Only query nodes that have actually installed the collection — others 400 on
    // Query (queryAll drops those rejections, but skipping avoids the wasted calls).
    const coll = this.collection;
    const ids = next.nodes.filter((n) => coll in n.collections).map((n) => n.id);
    const raw = await queryAll(ids, coll, this.liveQuery);
    const out: Record<string, string> = {};
    for (const [id, v] of Object.entries(raw)) out[id] = fmtResult(v);
    this.liveResults = out;
  }

  private pushData() {
    if (!this.state) return;
    this.renderTopbar();
    this.graph.setData(this.state, this.selected, this.collection, this.liveQuery, this.liveResults);
    this.panel.setData(this.state, this.selected, this.collection, this.liveQuery);
  }

  private renderTopbar() {
    const s = this.state!;
    const colls = Object.keys(s.schema);
    const sig = JSON.stringify({ colls, collection: this.collection, hasPlan: this.hasPlan() });
    if (sig === this.topbarSig) return;
    this.topbarSig = sig;

    this.deployBtn.textContent = this.hasPlan() ? "Edit & redeploy" : "Deploy";

    this.collSelect.hidden = colls.length === 0;
    this.collSelect.replaceChildren();
    for (const c of colls) {
      const o = document.createElement("option");
      o.value = c;
      o.textContent = c;
      if (c === this.collection) o.selected = true;
      this.collSelect.appendChild(o);
    }
  }
}

// fmtResult renders a query result for display. Numbers cross the wire as exact
// rational strings already; booleans/strings are JSON values.
function fmtResult(v: unknown): string {
  if (typeof v === "string") return v;
  if (typeof v === "boolean") return String(v);
  return JSON.stringify(v);
}

customElements.define("sandbox-app", SandboxApp);
