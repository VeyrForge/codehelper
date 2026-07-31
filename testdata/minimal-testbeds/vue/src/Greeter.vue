<script setup lang="ts">
  import Button from './Button.vue'
  import { ref, computed } from 'vue'

  const props = defineProps<{ title: string }>()
  const emit = defineEmits<{ greet: [string] }>()

  const open = ref(true)
  const label = computed(() => props.title.toUpperCase())

  function greet() {
    emit('greet', props.title)
    helper()
  }

  function helper() {
    return props.title.toUpperCase()
  }
</script>

<template>
  <div class="hero" v-if="open">
    <h1>{{ label }}</h1>
    <Button @click="greet" />
    <Badge />
  </div>
</template>

<style>
  .hero { color: navy; }
</style>
