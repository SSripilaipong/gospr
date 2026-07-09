import { deployRaw, reset } from "../api";
import { toast } from "../toast";

const SAMPLE = `type T = vector rat0+
merge T = zip max
fn total v::T = reduce + 0 v
query T.Value = total
update T.Add k::rat0+ = local (+ k)
collection Counter = T`;

interface OpenOpts {
  nodes: string[];
  // Preselected target node (the one currently selected in the graph, if any).
  target: string | null;
  // True when a plan is already pinned — redeploying requires a reset.
  hasPlan: boolean;
  // Last source we deployed this session, if known (enables a true "edit").
  source: string | null;
}

// <deploy-modal> is the one place DSL code is authored. It is opened on demand from
// the topbar (deploy is a once-or-twice action), giving the editor real room instead
// of a cramped always-on textarea. Self-contained: it runs the deploy itself and
// dispatches "deployed" {code} on success so the app can remember the source + refresh.
//
// Redeploy is validity-preflighted: the server parses/builds BEFORE the one-shot 409
// conflict, so a POST that returns 400 means the code is bad (cluster untouched), and
// 409 means the code is valid but blocked — only then do we reset and redeploy. This
// guarantees a broken edit never wipes the running cluster.
export class DeployModal extends HTMLElement {
  private dialog!: HTMLDivElement;
  private titleEl!: HTMLHeadingElement;
  private warnEl!: HTMLParagraphElement;
  private errEl!: HTMLParagraphElement;
  private select!: HTMLSelectElement;
  private editor!: HTMLTextAreaElement;
  private submitBtn!: HTMLButtonElement;
  private lastFocused: HTMLElement | null = null;
  private hasPlan = false;

  connectedCallback() {
    this.innerHTML = `
      <div class="modal-backdrop" hidden>
        <div class="modal" role="dialog" aria-modal="true" aria-labelledby="deploy-title">
          <div class="modal-head">
            <h2 id="deploy-title">Deploy</h2>
            <button class="icon-btn modal-close" aria-label="Close dialog" type="button">✕</button>
          </div>
          <p class="modal-warn warn-banner" hidden></p>
          <div class="field">
            <label for="deploy-target">Target node</label>
            <select id="deploy-target"></select>
          </div>
          <div class="field">
            <label for="deploy-code">DSL code</label>
            <textarea id="deploy-code" class="code-editor" spellcheck="false"></textarea>
          </div>
          <p class="modal-err" role="alert" hidden></p>
          <div class="modal-actions">
            <button class="ghost modal-cancel" type="button">Cancel</button>
            <button class="primary modal-submit" type="button">Deploy</button>
          </div>
        </div>
      </div>`;

    this.dialog = this.querySelector(".modal-backdrop")!;
    this.titleEl = this.querySelector("#deploy-title")!;
    this.warnEl = this.querySelector(".modal-warn")!;
    this.errEl = this.querySelector(".modal-err")!;
    this.select = this.querySelector("#deploy-target")!;
    this.editor = this.querySelector("#deploy-code")!;
    this.submitBtn = this.querySelector(".modal-submit")!;

    this.querySelector(".modal-close")!.addEventListener("click", () => this.close());
    this.querySelector(".modal-cancel")!.addEventListener("click", () => this.close());
    this.dialog.addEventListener("click", (e) => {
      if (e.target === this.dialog) this.close(); // backdrop click
    });
    this.submitBtn.addEventListener("click", () => void this.submit());

    // Note: Tab in the editor moves focus normally (no tab-to-indent) so keyboard
    // users are never trapped in the textarea — the DSL needs no leading indentation.

    // Esc closes; Tab/Shift+Tab is trapped within the dialog.
    this.dialog.addEventListener("keydown", (e) => this.onKeydown(e));
  }

  open(opts: OpenOpts) {
    this.hasPlan = opts.hasPlan;
    this.lastFocused = (document.activeElement as HTMLElement) ?? null;

    // Target select.
    this.select.replaceChildren();
    for (const id of opts.nodes) {
      const o = document.createElement("option");
      o.value = id;
      o.textContent = id;
      if (id === opts.target) o.selected = true;
      this.select.appendChild(o);
    }

    // Title / warning / editor contents adapt to whether a plan exists and whether
    // we hold its source (the server stores schema only, never the DSL text).
    if (!opts.hasPlan) {
      this.titleEl.textContent = "Deploy";
      this.editor.value = opts.source ?? SAMPLE;
      this.warnEl.hidden = true;
      this.submitBtn.textContent = "Deploy";
    } else if (opts.source) {
      this.titleEl.textContent = "Edit & redeploy";
      this.editor.value = opts.source;
      this.showWarn();
      this.submitBtn.textContent = "Reset & redeploy";
    } else {
      this.titleEl.textContent = "Deploy different code";
      this.editor.value = SAMPLE;
      this.showWarn();
      this.submitBtn.textContent = "Reset & redeploy";
    }

    this.errEl.hidden = true;
    this.dialog.hidden = false;
    document.querySelector(".shell")?.setAttribute("inert", "");
    // Focus the editor so the user can type immediately.
    this.editor.focus();
    this.editor.setSelectionRange(this.editor.value.length, this.editor.value.length);
  }

  close() {
    this.dialog.hidden = true;
    document.querySelector(".shell")?.removeAttribute("inert");
    this.lastFocused?.focus();
  }

  private isOpen(): boolean {
    return !this.dialog.hidden;
  }

  private showWarn() {
    this.warnEl.hidden = false;
    this.warnEl.textContent =
      "A plan is already deployed. Redeploying resets the cluster and discards all node state.";
  }

  private showError(msg: string) {
    this.errEl.hidden = false;
    this.errEl.textContent = msg;
  }

  private async submit() {
    const target = this.select.value;
    const code = this.editor.value;
    this.errEl.hidden = true;
    this.submitBtn.disabled = true;
    try {
      const r = await deployRaw(target, code);
      if (r.status === 200) {
        this.succeed(code, target);
        return;
      }
      if (r.status === 409) {
        // Code is valid but a plan is pinned — only now is it safe to reset.
        await reset();
        const r2 = await deployRaw(target, code);
        if (r2.status === 200) {
          this.succeed(code, target);
          return;
        }
        this.showError(r2.message || "Redeploy failed after reset.");
        return;
      }
      // 400 (or anything else): invalid code — the running cluster is untouched.
      this.showError(r.message || "Deploy rejected.");
    } catch (e) {
      this.showError(String((e as Error).message));
    } finally {
      this.submitBtn.disabled = false;
    }
  }

  private succeed(code: string, target: string) {
    toast(this.hasPlan ? `Redeployed to ${target}` : `Deployed to ${target}`);
    this.dispatchEvent(
      new CustomEvent("deployed", { detail: { code, target }, bubbles: true }),
    );
    this.close();
  }

  private onKeydown(e: KeyboardEvent) {
    if (!this.isOpen()) return;
    if (e.key === "Escape") {
      e.preventDefault();
      this.close();
      return;
    }
    if (e.key !== "Tab") return;
    // Trap focus within the dialog.
    const focusables = Array.from(
      this.dialog.querySelectorAll<HTMLElement>(
        'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])',
      ),
    ).filter((el) => !el.hasAttribute("disabled") && el.offsetParent !== null);
    if (focusables.length === 0) return;
    const first = focusables[0];
    const last = focusables[focusables.length - 1];
    const active = document.activeElement as HTMLElement;
    if (e.shiftKey && active === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && active === last) {
      e.preventDefault();
      first.focus();
    }
  }
}

customElements.define("deploy-modal", DeployModal);
