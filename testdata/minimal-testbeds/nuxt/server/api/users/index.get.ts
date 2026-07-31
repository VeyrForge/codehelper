import { listUsers } from "../../../lib/users";

/** Nitro users API: defineEventHandler → listUsers. */
export default defineEventHandler(async () => {
  return listUsers();
});
