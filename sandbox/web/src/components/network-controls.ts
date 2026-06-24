import { reset, setSpeed, type SandboxState } from "../api";
import { toast } from "../toast";

// <network-controls> owns the message-delay field and the Reset button (rebuilds
// the cluster so new code can be deployed). Partitioning links is done by clicking
// them directly in the graph.
export class NetworkControls extends HTMLElement {
  private state: SandboxState | null = null;
  private lastSig = "";

  setData(state: SandboxState) {
    this.state = state;
    // Skip re-render on gossip/slot churn so the delay field keeps focus while typing.
    const sig = JSON.stringify({
      ids: state.nodes.map((n) => n.id),
      delayMs: state.delayMs,
    });
    if (sig === this.lastSig) return;
    this.lastSig = sig;
    this.render();
  }

  private refresh() {
    this.dispatchEvent(new CustomEvent("refresh", { bubbles: true }));
  }

  private render() {
    if (!this.state) return;
    const s = this.state;
    this.replaceChildren();

    const card = el("div", "card stack");
    card.appendChild(el("h2", "", "Network"));

    // Message delay (typed value, in ms).
    const speedWrap = el("div");
    speedWrap.appendChild(labelEl("Message delay (ms)"));
    const row = el("div", "row");
    const field = document.createElement("input");
    field.type = "number";
    field.min = "0";
    field.step = "100";
    field.inputMode = "numeric";
    field.value = String(s.delayMs);
    field.style.flex = "0 0 130px";
    const commit = async () => {
      const v = Math.max(0, Number(field.value) || 0);
      field.value = String(v);
      try {
        await setSpeed(v);
      } catch (e) {
        toast(String((e as Error).message), "error");
      }
    };
    field.addEventListener("change", commit);
    row.appendChild(field);
    row.appendChild(el("span", "hint", "ms"));
    speedWrap.appendChild(row);
    speedWrap.appendChild(
      el("p", "hint", "Click a link in the graph to partition / reconnect it."),
    );
    card.appendChild(speedWrap);

    // Reset.
    const resetWrap = el("div");
    resetWrap.appendChild(el("h3", "", "Cluster"));
    resetWrap.appendChild(
      el("p", "hint", "Reset tears down all nodes and starts a fresh, empty cluster — the way to deploy different code."),
    );
    const resetBtn = el("button", "danger", "Reset cluster") as HTMLButtonElement;
    resetBtn.addEventListener("click", async () => {
      if (!confirm("Reset the cluster? All deployed code and state will be discarded.")) return;
      resetBtn.disabled = true;
      try {
        await reset();
        toast("Cluster reset");
        this.refresh();
      } catch (e) {
        toast(String((e as Error).message), "error");
      } finally {
        resetBtn.disabled = false;
      }
    });
    resetWrap.appendChild(resetBtn);
    card.appendChild(resetWrap);

    this.appendChild(card);
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

customElements.define("network-controls", NetworkControls);
