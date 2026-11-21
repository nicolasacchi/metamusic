// sf2gen writes the generated test soundfont (one looped 440 Hz sine, root key 69) used by e2e and dev runs in place of the
// real General MIDI soundfont. No SF2 file is ever committed.
//
//	go run ./tools/sf2gen -out /tmp/test.sf2 [-release 1s] [-presets 1]
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "sf2gen:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("sf2gen", flag.ContinueOnError)
	out := fs.String("out", "", "output file (absolute or relative path, required)")
	release := fs.Duration("release", 0, "volume envelope release (0 = instant)")
	presets := fs.Int("presets", 1, "number of General MIDI programs (1..128)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" || fs.NArg() > 0 {
		return fmt.Errorf("usage: sf2gen -out <file> [-release <duration>] [-presets <n>]")
	}
	return write(filepath.Clean(*out), *release, *presets)
}

func write(path string, release time.Duration, presets int) error {
	return dsptest.WriteSF2(path, dsptest.SF2Options{Release: release, Presets: presets})
}
