/** Shared user leaf used by server/api and composables. */
export function listUsers(): { id: number; name: string }[] {
  return [
    { id: 1, name: "alice" },
    { id: 2, name: "bob" },
  ];
}
