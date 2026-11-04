<script lang="ts">
  interface Props {
    trackId: string;
    /** Seconds into the take the note is pinned to; null for none. */
    atSecond: number | null;
  }
  let { trackId, atSecond }: Props = $props();
</script>

<!-- Scaffold (M5-F2): the optional voice lane (M5-V1 server, M5-W2 web) fills it (C259, C262): MediaRecorder to base64 through
     lib/voiceApi.ts. Not mounted in the comment composer until then. -->
<div data-testid="voice-recorder" data-track={trackId} data-at={atSecond ?? ""} hidden></div>
