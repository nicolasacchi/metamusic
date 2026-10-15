package muses

import "github.com/nicolasacchi/metamusic/internal/textfold"

// Key is the reference-Muse identity: textfold.Norm(title) + "\x1f" + textfold.Norm(artist), the artist being ""
// when nil (D-L1-5, D-L1-33). A nil artist and an empty artist are the same key; "no artist" and an artist differ.
func Key(title string, artist *string) string {
	a := ""
	if artist != nil {
		a = textfold.Norm(*artist)
	}
	return textfold.Norm(title) + "\x1f" + a
}

// TitleFold is the keyset-order column of a Muse (title_fold = Norm(title)).
func TitleFold(title string) string { return textfold.Norm(title) }
