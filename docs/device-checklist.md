# Real-device checklist (template)

Checks CI cannot emulate. Copy this file per release, fill in the header and tick each item.

- Build / version:
- Date:
- Device and OS version:
- Browser:
- Server URL: `https://mm.example.com`

## Install and login
- [ ] Page loads over HTTPS; no mixed-content warnings
- [ ] Login works; session survives app restart
- [ ] PWA install / "add to home screen" works (if applicable)

## Playback
- [ ] Track plays with the screen locked
- [ ] Lock-screen / notification controls show title and work (play, pause, seek)
- [ ] Seeking within a long track is accurate and responsive
- [ ] Loudness is consistent between tracks (iOS ignores `audio.volume`; normalisation is baked in)
- [ ] Lossless (FLAC) playback works where supported; MP3 fallback otherwise
- [ ] Switching network (Wi-Fi to cellular) does not break playback

## Layout and input
- [ ] Portrait and landscape layouts usable; safe areas respected
- [ ] Touch targets comfortable; no hover-only controls
- [ ] Dark and light themes legible

## M1 (import and listen)
- [ ] DC-M1-1 install the PWA (iPhone + Android): icon, name, standalone window
- [ ] DC-M1-2 play a 24-bit/48 kHz FLAC master; seek by waveform; seek by lock-screen scrubber
- [ ] DC-M1-3 lock-screen controls: play/pause/next/prev work; no +-10 s buttons
- [ ] DC-M1-4 auto-advance with the screen locked (record result; known WebKit bugs)
- [ ] DC-M1-5 forward-auth session expiry while playing: audio continues; the "sign in again" banner appears; no automatic reload while playing; tapping the banner reloads into login. Airplane mode toggled while playing: no reload, only the offline notice
- [ ] DC-M1-6 device probe: the first Play tap runs it; Settings shows `ok` on both phones (an `inconclusive` result is recorded as such and never downgrades); `crossorigin="use-credentials"` manifest variant tried

## M2a (judge, edit, parity)
- [ ] DC-M2a-1 iPhone, installed PWA (standalone): launch => `/start` chooser => pair with a code typed from `/human/devices` on the desktop => library, playback, rating and comments work without SSO
- [ ] DC-M2a-2 the paired device survives a 13 h gap: open the app after >= 13 h => still signed in, no SSO prompt, playback starts
- [ ] DC-M2a-3 Android: scan the QR in the camera => Chrome pairs => the installed PWA is paired too (shared jar); record the result
- [ ] DC-M2a-4 while playing on the phone, "comment here" then tap the timestamp => the player jumps
- [ ] DC-M2a-5 airplane mode while playing => reconnect => the listen events appear in the track context
- [ ] DC-M2a-6 playlist ZIP downloads on the phone; the MP3s show their current titles in the Files app
- [ ] DC-M2a-7 revoking the device on the desktop => the phone shows the `/start` chooser on its next request

## M2b

- [ ] DC-M2b-1 iPhone (ringer on silent) and Android: a blind duel plays both clips in sync; toggling on a drum-heavy passage gives no flam, echo, click or gap
- [ ] DC-M2b-2 24-bit FLAC clips decode in Safari (browser and installed PWA) and Chrome on Android; ten consecutive duels without a reload or crash
- [ ] DC-M2b-3 the big toggle and verdict buttons work one-handed; locking the screen during a duel and returning resumes correctly
- [ ] DC-M2b-4 the spectrogram is readable on a phone; a tap seeks the player
- [ ] DC-M2b-5 the main player pauses when a duel starts and can be resumed afterwards

## M2c

- [ ] DC-M2c-1 iPhone and Android: a lossy reference track (MP3 master) plays from the library and the track page, seeks by tapping the waveform, and keeps playing with the screen locked; the quality switch is not offered for it
- [ ] DC-M2c-2 a lossy track and a FLAC reference in one blind duel play in sync and at matched loudness
- [ ] DC-M2c-3 the "recipe unknown" and "lossy reference" badges are readable on the phone and the library filters find the track

## M3

- [ ] DC-M3-1 an agent batch's ntfy "Review" action opens `/human/confirm/<batch>` on the phone, passes 2FA and approves the batch
- [ ] DC-M3-2 the three-click new-seed variant works on iPhone and Android: the child appears under its source and plays
- [ ] DC-M3-3 terminating a running job from a paired phone (device principal, write scope) shows it `cancelled`
- [ ] DC-M3-4 the trip banner and the "Set max workers to 0" push arrive on the phone (provoked with the fake backend's `fake:stuck_worker` on a dev instance)

## M4a

- [ ] DC-M4a-1 claude.ai web: add the custom connector (MCP URL and client id only), sign in with two factors, accept the consent page; "search my tracks for X" runs `search_tracks`; a requested comment appears on the track page authored by the client label
- [ ] DC-M4a-2 claude.ai mobile: the connector works after sign-in on the phone; a session the next day works without a new login (if not, set `MM_OIDC_SCOPES="openid offline_access metamusic"` and repeat; record which setting works)
- [ ] DC-M4a-3 ChatGPT (if the account has connectors): predefined client; the same two calls
- [ ] DC-M4a-4 Amperfy on iPhone: create a client on `/human/subsonic` (token login), stream format Raw; a 24-bit/48 kHz master plays and seeks; pause 15 minutes then resume and seek works; rating shows as human stars; starring sets final
- [ ] DC-M4a-5 Symfonium or Tempus on Android: API key login; the full sync finishes; an offline album download works; a bookmark with a note becomes a timestamped comment
- [ ] DC-M4a-6 installed PWA on iPhone and Android: sync the offline set on Wi-Fi, airplane mode, relaunch from the home screen: `/offline` opens and plays with seeking; record the `storage.persist()` result
- [ ] DC-M4a-7 Media Session: the lock screen shows the generated cover art
- [ ] DC-M4a-8 revoking the connector on `/human/connectors`: the next tool call fails and the request log shows 403 `CONNECTOR_REVOKED`; restoring works again

## L1

- [ ] DC-L1-1 pin a Pick from a paired phone: the Pick badge moves to the pinned take and the history shows the pin; unpinning restores the derived Pick
- [ ] DC-L1-2 the Board is usable on a phone: horizontal scroll works, targets are at least 44 px, and selection and the Estimate hand-off reach the generate page

## Notes
