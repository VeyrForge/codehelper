<script>
  import Toggle from './Toggle.svelte';
  import Card from './Card.svelte';

  export let title = 'Home';
</script>

<style>
  .page { padding: 1rem; }
</style>

<div class="page">
  <h1>{title}</h1>
  <Toggle />
  <Card />
</div>
