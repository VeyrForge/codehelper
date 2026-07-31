import { listUsers } from "./users";

/** getUsers → listUsers (controller→service). */
export async function getUsers() {
  return listUsers();
}
