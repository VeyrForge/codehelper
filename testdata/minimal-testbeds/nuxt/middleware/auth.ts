/** Nuxt route middleware (role=middleware). */
export default defineNuxtRouteMiddleware((to) => {
  if (to.path.startsWith("/admin")) {
    return navigateTo("/");
  }
});
