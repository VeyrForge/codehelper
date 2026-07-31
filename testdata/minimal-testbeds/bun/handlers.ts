import { greet } from "./greet";

/** Edge health handler (role=edge_handler via *Handler suffix). */
export function healthHandler(_req: Request): Response {
  return new Response(JSON.stringify({ ok: true }), {
    headers: { "content-type": "application/json" },
  });
}

/** Edge fetch leaf → greet. */
export function greetHandler(_req: Request): Response {
  return new Response(greet("bun"));
}
