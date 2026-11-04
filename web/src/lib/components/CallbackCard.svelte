<script lang="ts">
  interface Props {
    id: string;
    navigate: (to: string) => void;
  }
  let { id }: Props = $props(); // lane M5-W reads navigate from the props too
</script>

<!-- Scaffold (M5-F2): lane M5-W2 fills it (C262). -->
<section data-testid="callback-card" data-callback={id}></section>
