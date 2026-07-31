<script setup lang="ts">
/** Probe densify page: binds API HTML (XSS sink pair for SPA checklist). */
const { data } = await useFetch("/api/probe", { query: { q: "x" } });
const html = computed(() => (data.value as { html?: string } | null)?.html ?? "");
</script>

<template>
  <div>
    <div v-html="html"></div>
  </div>
</template>
