export function listUsers(): { id: number; name: string }[] {
  return [{ id: 1, name: "probe" }];
}

export function greet(name: string): string {
  return `hello ${name}`;
}
