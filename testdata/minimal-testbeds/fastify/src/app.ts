import Fastify from "fastify";
import { getUser, listUsers } from "./users";

const app = Fastify();

app.get("/health", async () => ({ ok: true }));
app.get("/users", listUsers);
app.get("/users/:id", async (req) => {
  const id = Number((req.params as { id: string }).id);
  return getUser(id);
});

export default app;
