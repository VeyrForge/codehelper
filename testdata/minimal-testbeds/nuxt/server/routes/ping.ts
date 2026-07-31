/**
 * Nitro server route densify: defineEventHandler → pingPayload.
 */
function pingPayload() {
  return { ok: true, route: "ping" };
}

export default defineEventHandler(() => {
  return pingPayload();
});
