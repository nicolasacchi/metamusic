package subsonic

import (
	"context"
	"errors"
	"slices"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/view"
)

// extensions are the advertised OpenSubsonic extensions (C108.6), sorted by name.
var extensions = []ExtensionInfo{
	{Name: "apiKeyAuthentication", Versions: []int{1}},
	{Name: "formPost", Versions: []int{1}},
	{Name: "playbackReport", Versions: []int{1}},
	{Name: "songLyrics", Versions: []int{1}},
}

// folders are the three virtual music folders (D-M4a-10).
var folders = []MusicFolder{{ID: 1, Name: "Library"}, {ID: 2, Name: "Drafts"}, {ID: 3, Name: "Discarded"}}

// visibleFolders are the folders the caller sees: a Listener's client has no Discarded folder (C308.6).
func visibleFolders(ctx context.Context) []MusicFolder {
	if view.FromContext(ctx).HideDiscarded {
		return slices.Clone(folders[:2])
	}
	return slices.Clone(folders)
}

func (s *server) registerSystem() {
	s.add("ping", false, func(*call) error { return nil })
	s.add("getLicense", false, func(c *call) error {
		c.resp.License = &License{Valid: true}
		return nil
	})
	s.add("getOpenSubsonicExtensions", false, func(c *call) error {
		c.resp.OpenSubsonicExtensions = slices.Clone(extensions)
		return nil
	})
	s.add("tokenInfo", false, func(c *call) error {
		cl, err := c.client()
		if err != nil {
			return err
		}
		c.resp.TokenInfo = &TokenInfo{Username: cl.Username}
		return nil
	})
	s.add("getUser", false, func(c *call) error {
		name := c.str("username")
		if name == "" {
			return fail(errMissingParam, "required parameter is missing: username")
		}
		cl, err := c.client()
		if err != nil {
			return err
		}
		if name != cl.Username {
			return fail(errNotAuthorized, "the client is not authorized to see other users")
		}
		w := c.principal.Has(auth.ScopeWrite)
		c.resp.User = &User{Username: cl.Username, ScrobblingEnabled: w, PlaylistRole: w, CommentRole: w,
			DownloadRole: true, CoverArtRole: true, StreamRole: true, Folder: userFolders(c.ctx)}
		return nil
	})
	s.add("getMusicFolders", false, func(c *call) error {
		c.resp.MusicFolders = &MusicFolders{MusicFolder: visibleFolders(c.ctx)}
		return nil
	})
}

// client loads the caller's credential row (its username is not part of the principal).
func (c *call) client() (auth.SubsonicClient, error) {
	if c.s.o.Creds == nil {
		return auth.SubsonicClient{}, fail(errGeneric, "service unavailable")
	}
	cl, err := c.s.o.Creds.Client(c.ctx, c.principal.ID)
	if errors.Is(err, auth.ErrNotFound) {
		return auth.SubsonicClient{}, fail(errBadCreds, "wrong username or password")
	}
	return cl, err
}

// userFolders is the folder id list of getUser, without the Discarded folder for a Listener's client.
func userFolders(ctx context.Context) []int {
	if view.FromContext(ctx).HideDiscarded {
		return []int{1, 2}
	}
	return []int{1, 2, 3}
}
