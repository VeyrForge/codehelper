import { listUsers } from "../lib/users";

/** Nuxt composable: useUsers → listUsers (auto-import style). */
export function useUsers() {
  const users = useState("users", () => listUsers());
  async function refresh() {
    users.value = listUsers();
  }
  return { users, refresh };
}
