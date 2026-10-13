<script lang="ts">
  import { STATUSES } from "../libraryQuery";
  import type { TrackStatus } from "../judge.svelte";

  interface Props {
    value: string;
    onchange: (status: TrackStatus) => void;
    label?: string;
  }
  let { value, onchange, label = "Status" }: Props = $props();
</script>

<label class="status">
  <span class="sr">{label}</span>
  <select {value} aria-label={label} data-testid="status-select" onchange={(e) => onchange(e.currentTarget.value as TrackStatus)}>
    {#each STATUSES as s (s)}
      <option value={s} selected={s === value}>{s}</option>
    {/each}
  </select>
</label>

<style>
  .sr {
    position: absolute;
    left: -9999px;
  }
  select {
    min-height: 2rem;
  }
</style>
