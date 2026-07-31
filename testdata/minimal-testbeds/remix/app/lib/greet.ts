export function greet(name: string): string {
  return `hello ${name}`;
}

export function saveGreeting(name: string): string {
  return greet(name);
}
