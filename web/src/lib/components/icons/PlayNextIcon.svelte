<script lang="ts">
  let { size = 24 }: { size?: number } = $props();
</script>

<svg
  width={size}
  height={size}
  viewBox="0 0 24 24"
  fill="none"
  stroke="currentColor"
  stroke-width="2"
  stroke-linecap="round"
  stroke-linejoin="round"
  aria-hidden="true"
  focusable="false"
>
  <polygon points="4 4 14 11 4 18 4 4" fill="currentColor" /><line x1="18" y1="5" x2="18" y2="17" /><line x1="12" y1="21" x2="22" y2="21" />
</svg>
