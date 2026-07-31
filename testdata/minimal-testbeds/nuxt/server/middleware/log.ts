/**
 * Nitro server middleware densify: defineEventHandler → setHeader.
 */
export default defineEventHandler((event) => {
  setHeader(event, "x-nuxt-probe", "1");
});
