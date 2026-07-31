import { route } from "./router";

function fetchHandler(req: Request): Response {
  return route(req);
}

Bun.serve({
  fetch: fetchHandler,
  error(err: Error) {
    return new Response(String(err), { status: 500 });
  },
});
