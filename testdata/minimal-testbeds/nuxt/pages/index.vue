<script setup lang="ts">
const { n, increment } = useCounter();
const { data: health } = await useFetch("/api/health");
</script>

<template>
  <div>
    <button @click="increment">{{ n }}</button>
    <pre>{{ health }}</pre>
  </div>
</template>
