export function listUsers(): { id: number; name: string }[] {
  return [{ id: 1, name: "probe" }];
}

export function getUser(id: number): { id: number; name: string } {
  return { id, name: "probe" };
}
