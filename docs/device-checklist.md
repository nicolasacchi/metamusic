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

## Notes
