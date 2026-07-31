function healthPayload(): { ok: boolean } {
  return { ok: true };
}

/** Nitro server route: defineEventHandler → healthPayload (probe surface). */
export default defineEventHandler(() => {
  return healthPayload();
});
