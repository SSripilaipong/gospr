import { invoke, setSpeed, type ParamSchema, type SandboxState } from "../api";
import { toast } from "../toast";

// <node-panel> is the right inspector rail. It has three regions:
//   1. Live label — a cluster-wide picker choosing which (param-less) query is shown
//      live on every node in the graph. Dispatches "set-live-query" {name|null}.
//   2. Selected node — invoke blocks (updates) for the selected node + collection.
//   3. Network — message-delay field + a hint that links are partitioned by clicking.
// Deploy lives in <deploy-modal>, not here, so the rail stays quiet between deploys.
//
// Like the other components it signature-guards render: it re-renders only when a
// field it displays changes, so the 750ms poll never wipes an in-progress input.
export class NodePanel extends HTMLElement {
  private state: SandboxState | null = null;
  private selected: string | null = null;
  private collection: string | null = null;
  private liveQuery: string | null = null;
  private lastSig = "";

  setData(
    state: SandboxState,
    selected: string | null,
    collection: string | null,
    liveQuery: string | null,
  ) {
    this.state = state;
    this.selected = selected;
    this.collection = collection;
    this.liveQuery = liveQuery;
    const sig = JSON.stringify({
      hasPlan: this.hasPlan(),
      selected,
      collection,
      liveQuery,
      ids: state.nodes.map((n) => n.id),
      schema: collection ? state.schema[collection] : null,
      delayMs: state.delayMs,
    });
    if (sig === this.lastSig) return;
    this.lastSig = sig;
    this.render();
  }

  private refresh() {
    this.dispatchEvent(new CustomEvent("refresh", { bubbles: true }));
  }

  private hasPlan(): boolean {
    return !!this.state && Object.keys(this.state.schema).length > 0;
  }

  private render() {
    if (!this.state) return;
    this.replaceChildren();

    if (!this.hasPlan()) {
      const card = el("div", "card");
      card.appendChild(el("h2", "", "Inspector"));
      card.appendChild(
        el("p", "empty", "Deploy a type to begin. Use the Deploy button in the top bar."),
      );
      this.appendChild(card);
      this.appendChild(this.networkCard());
      return;
    }

    this.appendChild(this.liveLabelCard());
    this.appendChild(this.selectedNodeCard());
    this.appendChild(this.networkCard());
  }

  private liveLabelCard(): HTMLElement {
    const card = el("div", "card stack");
    card.appendChild(el("h2", "", "Live label"));
    card.appendChild(
      el("p", "hint", "Show a query's result live on every node in the graph."),
    );

    const cs = this.collection ? this.state!.schema[this.collection] : undefined;
    const queries = cs ? Object.keys(cs.queries) : [];
    if (queries.length === 0) {
      card.appendChild(el("p", "empty", "This collection has no queries."));
      return card;
    }

    const select = document.createElement("select");
    select.setAttribute("aria-label", "Live label query");
    const off = document.createElement("option");
    off.value = "";
    off.textContent = "Off";
    select.appendChild(off);
    for (const q of queries) {
      const o = document.createElement("option");
      o.value = q;
      o.textContent = q;
      if (q === this.liveQuery) o.selected = true;
      select.appendChild(o);
    }
    select.addEventListener("change", () => {
      this.dispatchEvent(
        new CustomEvent("set-live-query", {
          detail: { name: select.value || null },
          bubbles: true,
        }),
      );
    });
    card.appendChild(select);
    return card;
  }

  private selectedNodeCard(): HTMLElement {
    const card = el("div", "card stack");
    if (!this.selected) {
      card.appendChild(el("h2", "", "Inspect node"));
      card.appendChild(el("p", "empty", "Select a node in the graph to invoke updates."));
      return card;
    }

    const node = this.selected;
    card.appendChild(el("h2", "", `Node ${node}`));

    const cs = this.collection ? this.state!.schema[this.collection] : undefined;
    const updates = cs ? Object.entries(cs.updates) : [];
    if (updates.length === 0) {
      card.appendChild(el("p", "empty", "This collection has no updates."));
      return card;
    }
    for (const [action, params] of updates) {
      card.appendChild(this.methodBlock(node, this.collection!, action, params));
    }
    return card;
  }

  private methodBlock(
    node: string,
    collection: string,
    name: string,
    params: ParamSchema[],
  ): HTMLElement {
    const block = el("div", "method-block");
    block.appendChild(el("h4", "", name));

    const inputs: HTMLInputElement[] = [];
    if (params.length > 0) {
      const grid = el("div", "params");
      for (const p of params) {
        const w = el("div");
        w.appendChild(labelEl(`${p.name} (${p.type})`));
        const input = document.createElement("input");
        input.placeholder = `e.g. 5 or 1/2`;
        w.appendChild(input);
        inputs.push(input);
        grid.appendChild(w);
      }
      block.appendChild(grid);
    }

    const btn = el("button", "primary", "Invoke") as HTMLButtonElement;
    btn.addEventListener("click", async () => {
      const values = inputs.map((i) => i.value.trim());
      btn.disabled = true;
      try {
        await invoke(node, collection, name, values);
        toast(`${name} applied on ${node}`);
        this.refresh();
      } catch (e) {
        toast(String((e as Error).message), "error");
      } finally {
        btn.disabled = false;
      }
    });
    block.appendChild(btn);
    return block;
  }

  private networkCard(): HTMLElement {
    const card = el("div", "card stack");
    card.appendChild(el("h2", "", "Network"));

    const speedWrap = el("div");
    speedWrap.appendChild(labelEl("Message delay (ms)"));
    const row = el("div", "row");
    const field = document.createElement("input");
    field.type = "number";
    field.min = "0";
    field.step = "100";
    field.inputMode = "numeric";
    field.value = String(this.state!.delayMs);
    field.style.flex = "0 0 130px";
    field.addEventListener("change", async () => {
      const v = Math.max(0, Number(field.value) || 0);
      field.value = String(v);
      try {
        await setSpeed(v);
      } catch (e) {
        toast(String((e as Error).message), "error");
      }
    });
    row.appendChild(field);
    row.appendChild(el("span", "hint", "ms"));
    speedWrap.appendChild(row);
    speedWrap.appendChild(
      el("p", "hint", "Click a link in the graph to partition / reconnect it."),
    );
    card.appendChild(speedWrap);
    return card;
  }
}

function el(tag: string, className = "", text = ""): HTMLElement {
  const e = document.createElement(tag);
  if (className) e.className = className;
  if (text) e.textContent = text;
  return e;
}

function labelEl(text: string): HTMLElement {
  return el("label", "", text);
}

customElements.define("node-panel", NodePanel);
