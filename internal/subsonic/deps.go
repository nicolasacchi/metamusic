package subsonic

import (
	"context"

	"github.com/nicolasacchi/metamusic/internal/albums"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/listening"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/playlists"
)

// judger is the part of *judgments.Service the write endpoints use (an interface so tests can inject failures).
type judger interface {
	UpdateTrack(ctx context.Context, p auth.Principal, trackID string, title, status *string) error
	Rate(ctx context.Context, p auth.Principal, trackID string, stars *int, verdict *string) (library.TrackRating, error)
	Ratings(ctx context.Context, trackID string) (library.TrackRating, error)
	AddComment(ctx context.Context, p auth.Principal, src string, trackID string, in judgments.NewComment) (judgments.Comment, error)
}

// listener is the part of *listening.Service the scrobble endpoints use.
type listener interface {
	Log(ctx context.Context, p auth.Principal, src string, evs []listening.Event) (accepted, duplicates int, rejected []listening.Rejected, err error)
}

// playlister is the part of *playlists.Service the playlist endpoints use.
type playlister interface {
	Create(ctx context.Context, p auth.Principal, in playlists.NewPlaylist) (playlists.Playlist, error)
	Update(ctx context.Context, p auth.Principal, id string, in playlists.PlaylistPatch) (playlists.Playlist, error)
}

// mediaResolver is the part of *library.Service the stream endpoint uses.
type mediaResolver interface {
	Resolve(ctx context.Context, sha, ext string) (serve.Object, error)
}

// albumSource is the part of *albums.Service the Record albums use (C126.7).
type albumSource interface {
	List(ctx context.Context, kinds ...albums.Kind) ([]albums.Album, error)
	Get(ctx context.Context, id string) (albums.Album, []albums.Song, error)
	Token(ctx context.Context) (string, error)
}
