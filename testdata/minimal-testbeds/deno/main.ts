import { route } from "./router.ts";

function handler(req: Request): Response {
  return route(req);
}

Deno.serve(handler);
