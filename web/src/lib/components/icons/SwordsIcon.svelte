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
  <line x1="4" y1="4" x2="17" y2="17" /><line x1="20" y1="4" x2="7" y2="17" /><line x1="14" y1="19" x2="19" y2="14" /><line x1="10" y1="19" x2="5" y2="14" /><line x1="3" y1="21" x2="6" y2="18" /><line x1="21" y1="21" x2="18" y2="18" />
</svg>
