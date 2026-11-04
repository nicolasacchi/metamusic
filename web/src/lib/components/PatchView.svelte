<script lang="ts">
  interface Props {
    id: string;
    navigate: (to: string) => void;
  }
  let { id }: Props = $props(); // lane M5-W reads navigate from the props too
</script>

<!-- Scaffold (M5-F2): lane M5-W1 fills it (diff, scope, renders, callbacks). -->
<section data-testid="patch-view" data-patch={id}></section>
