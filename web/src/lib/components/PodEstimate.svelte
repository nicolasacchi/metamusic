<script lang="ts">
  import { durationText } from "../bulkView";

  interface Props {
    pod: { boot_s: number; deadline_s: number };
    backend: string;
  }
  let { pod, backend }: Props = $props();
</script>

<section class="pod" data-testid="pod-estimate" aria-label="Pod">
  <h3>One pod for the whole batch</h3>
  <ul>
    <li data-testid="pod-boot">Start-up allowance <b>{durationText(pod.boot_s)}</b>: the pod is billed from creation, before the first take starts.</li>
    <li data-testid="pod-deadline">Whole batch within <b>{durationText(pod.deadline_s)}</b> of its creation, then the pod is stopped whatever is left.</li>
  </ul>
  <p data-testid="pod-overhead">
    The pod's start-up, idle and safety time are part of Expected, Maximum and the Ceiling above, and the Retry allowance covers one re-creation of the pod. Nothing of
    it is hidden or billed on top.
  </p>
  <p class="muted" data-testid="pod-canary-note">
    Backend {backend}. A pod canary (the self-termination check of the image) always waits for the Owner's approval, whoever asks, and no night-shift mandate covers a pod.
  </p>
</section>

<style>
  .pod {
    border: 1px solid #3a3f4b;
    border-radius: 6px;
    padding: 0.2rem 0.8rem;
    margin: 0.8rem 0;
  }
  .pod h3 {
    margin: 0.4rem 0;
  }
</style>
