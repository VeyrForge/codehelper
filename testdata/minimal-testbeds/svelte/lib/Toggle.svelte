<script lang="ts">
  export function toggle(open: boolean): boolean {
    return !open;
  }

  export function greet(name: string): string {
    return format(name);
  }

  function format(name: string): string {
    return name.trim();
  }
</script>

<button onclick={toggle}>Toggle</button>
