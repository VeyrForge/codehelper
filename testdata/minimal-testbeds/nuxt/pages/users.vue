<script setup lang="ts">
definePageMeta({ middleware: "auth" });
const { users, refresh } = useUsers();
</script>

<template>
  <ul>
    <li v-for="u in users" :key="u.id">{{ u.name }}</li>
  </ul>
  <button @click="refresh">Refresh</button>
</template>
