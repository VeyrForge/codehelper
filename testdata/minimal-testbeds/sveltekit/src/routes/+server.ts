import { healthPayload } from "$lib/greet";

export async function GET() {
  return new Response(JSON.stringify(healthPayload()));
}
