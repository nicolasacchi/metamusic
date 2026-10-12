// Global player state. The single <audio> element lives in App.svelte's player bar
// and is registered here; it is never re-created on navigation.
class Player {
  el = $state<HTMLAudioElement | null>(null);
  title = $state("Nothing playing");
  src = $state("");

  attach(el: HTMLAudioElement | null) {
    this.el = el;
  }
}

export const player = new Player();
