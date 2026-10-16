package songview_test

import (
	"time"

	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/songs/songstest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type musesMuse = muses.Muse

func museListAll() muses.ListFilter { return muses.ListFilter{IncludeHidden: true, Limit: 200} }

const (
	songStateSQL  = store.SongStateSQL
	songStateCase = store.SongStateCase
)

func nowFn(l *songstest.Lib) func() time.Time { return func() time.Time { return l.Now } }
