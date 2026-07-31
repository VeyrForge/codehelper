export function greet(name: string): string {
  return `hello ${name}`;
}

export function healthPayload(): { ok: boolean } {
  return { ok: true };
}
