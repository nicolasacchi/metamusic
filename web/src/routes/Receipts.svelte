<script lang="ts">
  import ReceiptsTable from "../lib/components/ReceiptsTable.svelte";
</script>

<!-- Web-only (C262, D-M5-R3-5): get_audit pages grouped by principal with a 30 minute gap, money from list_batches. -->
<h1>Receipts</h1>
<ReceiptsTable />
