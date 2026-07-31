import { Hono } from "hono";
import { greet, listUsers } from "./users";

const app = new Hono();

app.get("/health", (c) => c.json({ ok: true }));
app.get("/users", (c) => c.json(listUsers()));
app.get("/greet/:name", (c) => c.text(greet(c.req.param("name"))));

export default app;
