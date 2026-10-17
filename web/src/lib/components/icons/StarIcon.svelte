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
  <polygon points="12 3 14.8 9 21 9.7 16.4 14 17.7 20.5 12 17.2 6.3 20.5 7.6 14 3 9.7 9.2 9 12 3" />
</svg>
