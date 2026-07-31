<script setup>
  defineProps({ label: String })
  function press() {}
</script>

<template>
  <button @click="press">{{ label }}</button>
</template>
