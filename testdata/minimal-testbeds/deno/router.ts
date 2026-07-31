import { greetHandler, healthHandler } from "./handlers.ts";

/** Route dispatcher: pathname → named edge handlers. */
export function route(req: Request): Response {
  const path = new URL(req.url).pathname;
  if (path === "/health") {
    return healthHandler(req);
  }
  return greetHandler(req);
}
