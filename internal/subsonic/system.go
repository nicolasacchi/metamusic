package subsonic

import (
	"slices"

	"github.com/nicolasacchi/metamusic/internal/auth"
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
			DownloadRole: true, CoverArtRole: true, StreamRole: true, Folder: []int{1, 2, 3}}
		return nil
	})
	s.add("getMusicFolders", false, func(c *call) error {
		c.resp.MusicFolders = &MusicFolders{MusicFolder: slices.Clone(folders)}
		return nil
	})
}

// client loads the caller's credential row (its username is not part of the principal).
func (c *call) client() (auth.SubsonicClient, error) {
	if c.s.o.Creds == nil {
		return auth.SubsonicClient{}, fail(errGeneric, "service unavailable")
	}
	list, err := c.s.o.Creds.List(c.ctx)
	if err != nil {
		return auth.SubsonicClient{}, err
	}
	for _, cl := range list {
		if cl.ID == c.principal.ID {
			return cl, nil
		}
	}
	return auth.SubsonicClient{}, fail(errBadCreds, "wrong username or password")
}
