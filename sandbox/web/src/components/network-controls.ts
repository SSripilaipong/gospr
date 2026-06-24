import { reset, setLink, setSpeed, type SandboxState } from "../api";
import { toast } from "../toast";
import { linkKey } from "../util";

// <network-controls> owns the gossip-speed slider, per-pair link toggles, and the
// Reset button (rebuilds the cluster so new code can be deployed).
export class NetworkControls extends HTMLElement {
  private state: SandboxState | null = null;

  setData(state: SandboxState) {
    this.state = state;
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

    // Speed slider.
    const speedWrap = el("div");
    speedWrap.appendChild(labelEl(`Message delay: ${s.delayMs} ms`));
    const row = el("div", "slider-row");
    const slider = document.createElement("input");
    slider.type = "range";
    slider.min = "0";
    slider.max = "3000";
    slider.step = "100";
    slider.value = String(s.delayMs);
    slider.addEventListener("input", () => {
      speedWrap.firstElementChild!.textContent = `Message delay: ${slider.value} ms`;
    });
    slider.addEventListener("change", async () => {
      try {
        await setSpeed(Number(slider.value));
      } catch (e) {
        toast(String((e as Error).message), "error");
      }
    });
    row.appendChild(slider);
    speedWrap.appendChild(row);
    card.appendChild(speedWrap);

    // Link toggles.
    const ids = s.nodes.map((n) => n.id);
    const linksWrap = el("div");
    linksWrap.appendChild(el("h3", "", "Links"));
    if (ids.length < 2) {
      linksWrap.appendChild(el("p", "hint", "Need at least 2 nodes for links."));
    }
    for (let i = 0; i < ids.length; i++) {
      for (let j = i + 1; j < ids.length; j++) {
        const a = ids[i];
        const b = ids[j];
        const connected = s.links[linkKey(a, b)] ?? true;
        const lt = el("div", "link-toggle");
        const pair = el("span", "pair", `${a} ↔ ${b}`);
        const badge = el(
          "span",
          `badge ${connected ? "init" : "empty-b"}`,
          connected ? "● connected" : "✕ disconnected",
        );
        const btn = el("button", "", connected ? "Disconnect" : "Connect") as HTMLButtonElement;
        btn.addEventListener("click", async () => {
          try {
            await setLink(a, b, !connected);
            this.refresh();
          } catch (e) {
            toast(String((e as Error).message), "error");
          }
        });
        const left = el("div", "row");
        left.appendChild(pair);
        left.appendChild(badge);
        lt.appendChild(left);
        lt.appendChild(btn);
        linksWrap.appendChild(lt);
      }
    }
    card.appendChild(linksWrap);

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
