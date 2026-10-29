package tape

import "errors"

// The disk preflight (C128.4 step 1): tape.flac and final.flac coexist (Rewrite copies every frame), so the peak is two FLAC
// files of about 0.65 x PCM each. Free space must cover 1.3 x the PCM size of the whole tape plus a fixed margin.
const (
	pcmBytesPerSecond = Rate * 6 // 24-bit stereo
	preflightMargin   = 64 << 20
)

// errFreeUnknown is returned by freeBytes on a platform that cannot tell; the check is then skipped.
var errFreeUnknown = errors.New("tape: free disk space is unknown on this platform")

// DiskNeeded is the free space a render of items totalling durationS seconds requires: 1.3 x duration x 288 000 B + 64 MiB.
func DiskNeeded(durationS float64) uint64 {
	if durationS < 0 {
		durationS = 0
	}
	pcm := durationS * pcmBytesPerSecond
	return uint64(pcm*13/10) + preflightMargin
}

// checkDisk fails with TAPE_DISK_FULL when dir has less than needed bytes free. A platform that cannot tell skips the check.
func checkDisk(free func(string) (uint64, error), dir string, needed uint64) error {
	got, err := free(dir)
	if err != nil {
		if errors.Is(err, errFreeUnknown) {
			return nil
		}
		return renderErr(ErrDiskFull, 0, err)
	}
	if got < needed {
		return renderErr(ErrDiskFull, 0, errors.New("not enough free disk space"))
	}
	return nil
}
