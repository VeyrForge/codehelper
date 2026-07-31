import { greet, healthPayload } from "$lib/greet";

/** SvelteKit load → greet + healthPayload (probe surface). */
export async function load() {
  return {
    message: greet("sveltekit"),
    health: healthPayload(),
  };
}
