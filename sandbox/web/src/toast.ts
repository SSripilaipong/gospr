// Minimal toast: a single transient message bottom-right. Errors state the
// problem; feedback appears well within the Doherty threshold.

let timer: number | undefined;

export function toast(message: string, kind: "success" | "error" = "success"): void {
  let el = document.querySelector<HTMLDivElement>(".toast");
  if (!el) {
    el = document.createElement("div");
    document.body.appendChild(el);
  }
  el.className = `toast ${kind}`;
  el.innerHTML = `<div class="toast-title">${kind === "error" ? "Error" : "Done"}</div><div></div>`;
  el.lastElementChild!.textContent = message;

  if (timer) window.clearTimeout(timer);
  timer = window.setTimeout(() => {
    el?.remove();
    timer = undefined;
  }, kind === "error" ? 6000 : 3000);
}
