/**
 * Nuxt client plugin densify: defineNuxtPlugin → provideHello.
 */
function provideHello(_nuxtApp: unknown) {
  return "hello";
}

export default defineNuxtPlugin((nuxtApp) => {
  provideHello(nuxtApp);
});
