// API types mirroring sandbox/server.go responses, plus thin fetch wrappers.

export interface ParamSchema {
  name: string;
  type: string;
}

export interface CollectionSchema {
  updates: Record<string, ParamSchema[]>;
  queries: Record<string, ParamSchema[]>;
}

// liveLabelQueries returns the names of a collection's zero-param queries — the
// only ones a live label can poll, since the label calls each query with no
// params across every node each tick. A parameterized query would 400 on the
// arity check. Single source of truth so the picker and reconcile can't drift.
export function liveLabelQueries(
  schema: Record<string, CollectionSchema>,
  collection: string | null,
): string[] {
  const cs = collection ? schema[collection] : undefined;
  if (!cs) return [];
  return Object.keys(cs.queries).filter((q) => cs.queries[q].length === 0);
}

// A slot value is a scalar (exact-rational string) or a struct (a nested object
// of field -> SlotValue), matching the server's recursive wire encoding.
export type SlotValue = string | { [field: string]: SlotValue };

export interface NodeState {
  id: string;
  initialized: boolean;
  collections: Record<string, Record<string, SlotValue>>; // collection -> slot -> value
}

export interface SandboxState {
  nodes: NodeState[];
  schema: Record<string, CollectionSchema>;
  links: Record<string, boolean>; // "a|b" -> connected
  delayMs: number;
}

export interface FlightEvent {
  kind: "gossip" | "deploy" | "reset";
  from?: string;
  to?: string;
  status?: "inflight" | "delivered" | "dropped";
}

async function ok(res: Response): Promise<void> {
  if (!res.ok) {
    const text = await res.text();
    throw new Error(text || `${res.status} ${res.statusText}`);
  }
}

export async function getState(): Promise<SandboxState> {
  const res = await fetch("/api/sandbox/state");
  await ok(res);
  return res.json();
}

export interface DeployResult {
  ok: boolean;
  status: number;
  message: string;
}

// deployRaw returns the HTTP status instead of throwing, so the modal can branch:
// 200 = deployed, 400 = code rejected (keep current cluster), 409 = code is valid
// but a plan is already pinned (caller resets, then redeploys).
export async function deployRaw(target: string, code: string): Promise<DeployResult> {
  const res = await fetch("/api/sandbox/deploy", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ target, code }),
  });
  const message = res.ok ? "" : (await res.text()) || `${res.status} ${res.statusText}`;
  return { ok: res.ok, status: res.status, message };
}

export async function reset(): Promise<void> {
  await ok(await fetch("/api/sandbox/reset", { method: "POST" }));
}

export async function invoke(
  node: string,
  collection: string,
  action: string,
  params: string[],
): Promise<void> {
  const res = await fetch(
    `/api/sandbox/nodes/${node}/collections/${collection}/${action}`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ params }),
    },
  );
  await ok(res);
}

export async function query(
  node: string,
  collection: string,
  q: string,
  params: string[],
): Promise<unknown> {
  const qs = params.length ? `?params=${encodeURIComponent(params.join(","))}` : "";
  const res = await fetch(
    `/api/sandbox/nodes/${node}/collections/${collection}/${q}${qs}`,
  );
  await ok(res);
  return res.json();
}

// queryAll polls one (param-less) query across many nodes for the live per-node
// label. It is resilient: a node that hasn't installed the collection yet (deploy
// delay / partition) 400s on Query — those rejections are dropped via allSettled,
// so one unavailable node never breaks the poll cycle. Returns nodeId -> result for
// the nodes that answered.
export async function queryAll(
  nodes: string[],
  collection: string,
  q: string,
): Promise<Record<string, unknown>> {
  const settled = await Promise.allSettled(
    nodes.map(async (node) => [node, await query(node, collection, q, [])] as const),
  );
  const out: Record<string, unknown> = {};
  for (const r of settled) {
    if (r.status === "fulfilled") out[r.value[0]] = r.value[1];
  }
  return out;
}

export async function setLink(a: string, b: string, connected: boolean): Promise<void> {
  const res = await fetch("/api/sandbox/links", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ a, b, connected }),
  });
  await ok(res);
}

export async function setSpeed(delayMs: number): Promise<void> {
  const res = await fetch("/api/sandbox/speed", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ delayMs }),
  });
  await ok(res);
}

export function openEvents(onEvent: (ev: FlightEvent) => void): EventSource {
  const es = new EventSource("/api/sandbox/events");
  es.onmessage = (e) => {
    try {
      onEvent(JSON.parse(e.data) as FlightEvent);
    } catch {
      /* ignore malformed */
    }
  };
  return es;
}
