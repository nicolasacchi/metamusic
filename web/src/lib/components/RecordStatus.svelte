<script lang="ts">
  import { STATUS_LABEL, type RecordStatus } from "../recordView";

  let { status, hidden = false }: { status: RecordStatus; hidden?: boolean } = $props();
</script>

<span class="chip" class:ok={status === "published"} class:warn={status === "proposed"} data-testid="record-status" data-status={status}>{STATUS_LABEL[status]}</span>
{#if hidden}<span class="chip" data-testid="record-hidden">Hidden</span>{/if}
