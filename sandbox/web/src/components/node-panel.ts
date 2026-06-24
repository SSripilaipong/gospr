import {
  deploy,
  invoke,
  query,
  type ParamSchema,
  type SandboxState,
} from "../api";
import { toast } from "../toast";

const SAMPLE = `type T = vector rat0+
merge T = zip max
query T.Value = reduce + 0
update T.Add k::rat0+ = local (+ k)
collection Counter = T`;

// <node-panel> is the quiet secondary surface for interacting with the selected
// node: deploy code (before any plan), or invoke updates / run queries (after).
// Params render as one labeled input per declared param — bare exact-rational
// values, no quotes or commas.
export class NodePanel extends HTMLElement {
  private state: SandboxState | null = null;
  private selected: string | null = null;
  private lastSig = "";
  // In-progress deploy code, preserved across the re-renders that do happen (e.g.
  // changing the target node) so typing is never lost. Cleared after a deploy.
  private draft: string | null = null;

  setData(state: SandboxState, selected: string | null) {
    this.state = state;
    this.selected = selected;
    // Re-render only when something this panel actually shows changes — NOT on
    // gossip/slot churn (the panel displays no slot values), so typing survives.
    const sig = JSON.stringify({
      hasPlan: this.hasPlan(),
      selected,
      ids: state.nodes.map((n) => n.id),
      schema: state.schema,
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
      this.renderDeploy();
      return;
    }
    if (!this.selected) {
      const card = el("div", "card");
      card.appendChild(el("h2", "", "Interact"));
      const p = el("p", "empty", "Select a node in the graph to invoke methods or run queries.");
      card.appendChild(p);
      this.appendChild(card);
      return;
    }
    this.renderInteract();
  }

  private renderDeploy() {
    const card = el("div", "card stack");
    card.appendChild(el("h2", "", "Deploy"));
    card.appendChild(
      el("p", "empty", "No plan deployed yet. Deploy a type to begin — it propagates from the target node to its peers."),
    );

    const targetWrap = el("div");
    targetWrap.appendChild(labelEl("Target node"));
    const select = document.createElement("select");
    for (const n of this.state!.nodes) {
      const opt = document.createElement("option");
      opt.value = n.id;
      opt.textContent = n.id;
      if (n.id === this.selected) opt.selected = true;
      select.appendChild(opt);
    }
    targetWrap.appendChild(select);
    card.appendChild(targetWrap);

    const codeWrap = el("div");
    codeWrap.appendChild(labelEl("DSL code"));
    const ta = document.createElement("textarea");
    ta.value = this.draft ?? SAMPLE;
    ta.spellcheck = false;
    ta.addEventListener("input", () => {
      this.draft = ta.value;
    });
    codeWrap.appendChild(ta);
    card.appendChild(codeWrap);

    const btn = el("button", "primary", "Deploy") as HTMLButtonElement;
    btn.addEventListener("click", async () => {
      btn.disabled = true;
      try {
        await deploy(select.value, ta.value);
        this.draft = null;
        toast(`Deployed to ${select.value}`);
        this.refresh();
      } catch (e) {
        toast(String((e as Error).message), "error");
      } finally {
        btn.disabled = false;
      }
    });
    card.appendChild(btn);
    this.appendChild(card);
  }

  private renderInteract() {
    const node = this.selected!;
    const schema = this.state!.schema;

    const card = el("div", "card stack");
    card.appendChild(el("h2", "", `Node ${node}`));

    for (const [collection, cs] of Object.entries(schema)) {
      const section = el("div");
      section.appendChild(el("h3", "", collection));

      for (const [action, params] of Object.entries(cs.updates)) {
        section.appendChild(this.methodBlock(node, collection, action, params, "update"));
      }
      for (const [q, params] of Object.entries(cs.queries)) {
        section.appendChild(this.methodBlock(node, collection, q, params, "query"));
      }
      card.appendChild(section);
    }
    this.appendChild(card);
  }

  private methodBlock(
    node: string,
    collection: string,
    name: string,
    params: ParamSchema[],
    kind: "update" | "query",
  ): HTMLElement {
    const block = el("div", "method-block");
    const heading = el("h4", "", `${kind === "update" ? "▸" : "?"} ${name}`);
    block.appendChild(heading);

    const inputs: HTMLInputElement[] = [];
    if (params.length > 0) {
      const grid = el("div", "params");
      for (const p of params) {
        const w = el("div");
        w.appendChild(labelEl(`${p.name} (${p.type})`));
        const input = document.createElement("input");
        input.placeholder = `value, e.g. 5 or 1/2 (${p.type})`;
        w.appendChild(input);
        inputs.push(input);
        grid.appendChild(w);
      }
      block.appendChild(grid);
    }

    const result = el("pre", "result");
    result.style.display = "none";

    const btn = el(
      "button",
      kind === "update" ? "primary" : "",
      kind === "update" ? "Invoke" : "Run query",
    ) as HTMLButtonElement;
    btn.addEventListener("click", async () => {
      const values = inputs.map((i) => i.value.trim());
      btn.disabled = true;
      try {
        if (kind === "update") {
          await invoke(node, collection, name, values);
          toast(`${name} applied on ${node}`);
          this.refresh();
        } else {
          const v = await query(node, collection, name, values);
          result.style.display = "block";
          result.textContent = JSON.stringify(v);
          toast(`${name} → ${JSON.stringify(v)}`);
        }
      } catch (e) {
        toast(String((e as Error).message), "error");
      } finally {
        btn.disabled = false;
      }
    });

    block.appendChild(btn);
    block.appendChild(result);
    return block;
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
