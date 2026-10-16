// Field names and semantics follow the OpenSubsonic API description
// (third_party/opensubsonic/openapi.json, commit be131fb44a48fb346faa16f5a242785fdfe3052b, Apache-2.0,
// © OpenSubsonic contributors). Written for this project from that description; no code from GPL-licensed
// Subsonic servers or client libraries.

package subsonic

// annotationPayload holds the response elements of the playlist, bookmark, play queue and lyrics endpoints
// (M4a-S3). It is embedded in Response beside catalogPayload.
type annotationPayload struct {
	Playlists  *PlaylistList      `json:"playlists,omitempty" xml:"playlists,omitempty"`
	Playlist   *PlaylistWithSongs `json:"playlist,omitempty" xml:"playlist,omitempty"`
	Bookmarks  *Bookmarks         `json:"bookmarks,omitempty" xml:"bookmarks,omitempty"`
	PlayQueue  *PlayQueue         `json:"playQueue,omitempty" xml:"playQueue,omitempty"`
	Lyrics     *Lyrics            `json:"lyrics,omitempty" xml:"lyrics,omitempty"`
	LyricsList *LyricsList        `json:"lyricsList,omitempty" xml:"lyricsList,omitempty"`
}

// Playlist is one playlist of getPlaylists.
type Playlist struct {
	ID        string `json:"id" xml:"id,attr"`
	Name      string `json:"name" xml:"name,attr"`
	Comment   string `json:"comment,omitempty" xml:"comment,attr,omitempty"`
	Owner     string `json:"owner,omitempty" xml:"owner,attr,omitempty"`
	Public    bool   `json:"public" xml:"public,attr"`
	SongCount int    `json:"songCount" xml:"songCount,attr"`
	Duration  int    `json:"duration" xml:"duration,attr"`
	Created   string `json:"created" xml:"created,attr"`
	Changed   string `json:"changed" xml:"changed,attr"`
	CoverArt  string `json:"coverArt,omitempty" xml:"coverArt,attr,omitempty"`
}

// PlaylistList is the answer of getPlaylists.
type PlaylistList struct {
	Playlist []Playlist `json:"playlist" xml:"playlist"`
}

// PlaylistWithSongs is the answer of getPlaylist, createPlaylist.
type PlaylistWithSongs struct {
	Playlist
	Entry []Child `json:"entry,omitempty" xml:"entry"`
}

// Bookmark is one resume position.
type Bookmark struct {
	Position int64  `json:"position" xml:"position,attr"`
	Username string `json:"username" xml:"username,attr"`
	Comment  string `json:"comment,omitempty" xml:"comment,attr,omitempty"`
	Created  string `json:"created" xml:"created,attr"`
	Changed  string `json:"changed" xml:"changed,attr"`
	Entry    Child  `json:"entry" xml:"entry"`
}

// Bookmarks is the answer of getBookmarks.
type Bookmarks struct {
	Bookmark []Bookmark `json:"bookmark" xml:"bookmark"`
}

// PlayQueue is the answer of getPlayQueue.
type PlayQueue struct {
	Current   string  `json:"current,omitempty" xml:"current,attr,omitempty"`
	Position  int64   `json:"position,omitempty" xml:"position,attr,omitempty"`
	Username  string  `json:"username" xml:"username,attr"`
	Changed   string  `json:"changed" xml:"changed,attr"`
	ChangedBy string  `json:"changedBy" xml:"changedBy,attr"`
	Entry     []Child `json:"entry,omitempty" xml:"entry"`
}

// Lyrics is the answer of getLyrics.
type Lyrics struct {
	Artist string `json:"artist,omitempty" xml:"artist,attr,omitempty"`
	Title  string `json:"title,omitempty" xml:"title,attr,omitempty"`
	Value  string `json:"value" xml:",chardata"`
}

// LyricLine is one line of structured lyrics (never timed: D-M4a-13).
type LyricLine struct {
	Value string `json:"value" xml:",chardata"`
}

// StructuredLyrics is one lyrics document of getLyricsBySongId.
type StructuredLyrics struct {
	DisplayArtist string      `json:"displayArtist,omitempty" xml:"displayArtist,attr,omitempty"`
	DisplayTitle  string      `json:"displayTitle,omitempty" xml:"displayTitle,attr,omitempty"`
	Lang          string      `json:"lang" xml:"lang,attr"`
	Offset        int         `json:"offset" xml:"offset,attr"`
	Synced        bool        `json:"synced" xml:"synced,attr"`
	Line          []LyricLine `json:"line" xml:"line"`
}

// LyricsList is the answer of getLyricsBySongId.
type LyricsList struct {
	StructuredLyrics []StructuredLyrics `json:"structuredLyrics,omitempty" xml:"structuredLyrics"`
}
