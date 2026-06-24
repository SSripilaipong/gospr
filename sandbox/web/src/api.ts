// API types mirroring sandbox/server.go responses, plus thin fetch wrappers.

export interface ParamSchema {
  name: string;
  type: string;
}

export interface CollectionSchema {
  updates: Record<string, ParamSchema[]>;
  queries: Record<string, ParamSchema[]>;
}

export interface NodeState {
  id: string;
  initialized: boolean;
  collections: Record<string, Record<string, string>>; // collection -> slot -> ratString
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

export async function deploy(target: string, code: string): Promise<void> {
  const res = await fetch("/api/sandbox/deploy", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ target, code }),
  });
  await ok(res);
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
