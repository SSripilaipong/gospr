// linkKey mirrors the Go server's order-independent pair key.
export function linkKey(a: string, b: string): string {
  return a < b ? `${a}|${b}` : `${b}|${a}`;
}
