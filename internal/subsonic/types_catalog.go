// Field names and semantics follow the OpenSubsonic API description
// (third_party/opensubsonic/openapi.json, commit be131fb44a48fb346faa16f5a242785fdfe3052b, Apache-2.0,
// © OpenSubsonic contributors). Written for this project from that description; no code from GPL-licensed
// Subsonic servers or client libraries.

package subsonic

// catalogPayload holds the response elements of the catalog, browsing, list and search endpoints (M4a-S2). It is
// embedded in Response, so its fields sit beside the envelope's (encoding/json and encoding/xml both flatten
// embedded structs).
type catalogPayload struct {
	Indexes       *Indexes       `json:"indexes,omitempty" xml:"indexes,omitempty"`
	Directory     *Directory     `json:"directory,omitempty" xml:"directory,omitempty"`
	Artists       *ArtistsID3    `json:"artists,omitempty" xml:"artists,omitempty"`
	Artist        *ArtistWithAlb `json:"artist,omitempty" xml:"artist,omitempty"`
	Album         *AlbumWithSong `json:"album,omitempty" xml:"album,omitempty"`
	Song          *Child         `json:"song,omitempty" xml:"song,omitempty"`
	Genres        *Genres        `json:"genres,omitempty" xml:"genres,omitempty"`
	AlbumList     *AlbumList     `json:"albumList,omitempty" xml:"albumList,omitempty"`
	AlbumList2    *AlbumList2    `json:"albumList2,omitempty" xml:"albumList2,omitempty"`
	RandomSongs   *SongList      `json:"randomSongs,omitempty" xml:"randomSongs,omitempty"`
	SongsByGenre  *SongList      `json:"songsByGenre,omitempty" xml:"songsByGenre,omitempty"`
	SimilarSongs  *SongList      `json:"similarSongs,omitempty" xml:"similarSongs,omitempty"`
	SimilarSongs2 *SongList      `json:"similarSongs2,omitempty" xml:"similarSongs2,omitempty"`
	TopSongs      *SongList      `json:"topSongs,omitempty" xml:"topSongs,omitempty"`
	Starred       *Starred       `json:"starred,omitempty" xml:"starred,omitempty"`
	Starred2      *Starred2      `json:"starred2,omitempty" xml:"starred2,omitempty"`
	NowPlaying    *NowPlaying    `json:"nowPlaying,omitempty" xml:"nowPlaying,omitempty"`
	ScanStatus    *ScanStatus    `json:"scanStatus,omitempty" xml:"scanStatus,omitempty"`
	ArtistInfo    *ArtistInfo    `json:"artistInfo,omitempty" xml:"artistInfo,omitempty"`
	ArtistInfo2   *ArtistInfo    `json:"artistInfo2,omitempty" xml:"artistInfo2,omitempty"`
	AlbumInfo     *AlbumInfo     `json:"albumInfo,omitempty" xml:"albumInfo,omitempty"`
	SearchResult2 *SearchResult2 `json:"searchResult2,omitempty" xml:"searchResult2,omitempty"`
	SearchResult3 *SearchResult3 `json:"searchResult3,omitempty" xml:"searchResult3,omitempty"`

	Podcasts              *Podcasts              `json:"podcasts,omitempty" xml:"podcasts,omitempty"`
	NewestPodcasts        *NewestPodcasts        `json:"newestPodcasts,omitempty" xml:"newestPodcasts,omitempty"`
	InternetRadioStations *InternetRadioStations `json:"internetRadioStations,omitempty" xml:"internetRadioStations,omitempty"`
	Shares                *Shares                `json:"shares,omitempty" xml:"shares,omitempty"`
	Videos                *Videos                `json:"videos,omitempty" xml:"videos,omitempty"`
	ChatMessages          *ChatMessages          `json:"chatMessages,omitempty" xml:"chatMessages,omitempty"`
}

// Always-empty lists of features the library does not have; clients probe them while syncing.
type (
	Podcasts struct {
		Channel []struct{} `json:"channel" xml:"channel"`
	}
	NewestPodcasts struct {
		Episode []struct{} `json:"episode" xml:"episode"`
	}
	InternetRadioStations struct {
		InternetRadioStation []struct{} `json:"internetRadioStation" xml:"internetRadioStation"`
	}
	Shares struct {
		Share []struct{} `json:"share" xml:"share"`
	}
	Videos struct {
		Video []struct{} `json:"video" xml:"video"`
	}
	ChatMessages struct {
		ChatMessage []struct{} `json:"chatMessage" xml:"chatMessage"`
	}
)

// ItemGenre is one genre of an item.
type ItemGenre struct {
	Name string `json:"name" xml:"name,attr"`
}

// ArtistRef is the id and name of an artist inside a song or album (the required ArtistID3 fields only).
type ArtistRef struct {
	ID   string `json:"id" xml:"id,attr"`
	Name string `json:"name" xml:"name,attr"`
}

// ReplayGain is the replay gain data of a song; absent values are omitted.
type ReplayGain struct {
	TrackGain *float64 `json:"trackGain,omitempty" xml:"trackGain,attr,omitempty"`
	TrackPeak *float64 `json:"trackPeak,omitempty" xml:"trackPeak,attr,omitempty"`
}

// Contributor is a contributor artist (always empty here; the field must exist).
type Contributor struct {
	Role   string    `json:"role" xml:"role,attr"`
	Artist ArtistRef `json:"artist" xml:"artist"`
}

// DiscTitle is a disc title (always empty here).
type DiscTitle struct {
	Disc  int    `json:"disc" xml:"disc,attr"`
	Title string `json:"title" xml:"title,attr"`
}

// Child is a song or, with IsDir, an album shown as a directory.
type Child struct {
	ID                    string        `json:"id" xml:"id,attr"`
	Parent                string        `json:"parent,omitempty" xml:"parent,attr,omitempty"`
	IsDir                 bool          `json:"isDir" xml:"isDir,attr"`
	Title                 string        `json:"title" xml:"title,attr"`
	Album                 string        `json:"album,omitempty" xml:"album,attr,omitempty"`
	Artist                string        `json:"artist,omitempty" xml:"artist,attr,omitempty"`
	Track                 int           `json:"track,omitempty" xml:"track,attr,omitempty"`
	Year                  int           `json:"year,omitempty" xml:"year,attr,omitempty"`
	Genre                 string        `json:"genre,omitempty" xml:"genre,attr,omitempty"`
	CoverArt              string        `json:"coverArt,omitempty" xml:"coverArt,attr,omitempty"`
	Size                  int64         `json:"size,omitempty" xml:"size,attr,omitempty"`
	ContentType           string        `json:"contentType,omitempty" xml:"contentType,attr,omitempty"`
	Suffix                string        `json:"suffix,omitempty" xml:"suffix,attr,omitempty"`
	TranscodedContentType string        `json:"transcodedContentType,omitempty" xml:"transcodedContentType,attr,omitempty"`
	TranscodedSuffix      string        `json:"transcodedSuffix,omitempty" xml:"transcodedSuffix,attr,omitempty"`
	Duration              int           `json:"duration,omitempty" xml:"duration,attr,omitempty"`
	BitRate               int           `json:"bitRate,omitempty" xml:"bitRate,attr,omitempty"`
	BitDepth              int           `json:"bitDepth,omitempty" xml:"bitDepth,attr,omitempty"`
	SamplingRate          int           `json:"samplingRate,omitempty" xml:"samplingRate,attr,omitempty"`
	ChannelCount          int           `json:"channelCount,omitempty" xml:"channelCount,attr,omitempty"`
	Path                  string        `json:"path,omitempty" xml:"path,attr,omitempty"`
	PlayCount             int64         `json:"playCount,omitempty" xml:"playCount,attr,omitempty"`
	Played                string        `json:"played,omitempty" xml:"played,attr,omitempty"`
	Created               string        `json:"created,omitempty" xml:"created,attr,omitempty"`
	Starred               string        `json:"starred,omitempty" xml:"starred,attr,omitempty"`
	AlbumID               string        `json:"albumId,omitempty" xml:"albumId,attr,omitempty"`
	ArtistID              string        `json:"artistId,omitempty" xml:"artistId,attr,omitempty"`
	Type                  string        `json:"type,omitempty" xml:"type,attr,omitempty"`
	MediaType             string        `json:"mediaType,omitempty" xml:"mediaType,attr,omitempty"`
	UserRating            int           `json:"userRating,omitempty" xml:"userRating,attr,omitempty"`
	BPM                   int           `json:"bpm,omitempty" xml:"bpm,attr,omitempty"`
	Comment               string        `json:"comment,omitempty" xml:"comment,attr,omitempty"`
	SortName              string        `json:"sortName,omitempty" xml:"sortName,attr,omitempty"`
	MusicBrainzID         string        `json:"musicBrainzId" xml:"musicBrainzId,attr"`
	DisplayArtist         string        `json:"displayArtist,omitempty" xml:"displayArtist,attr,omitempty"`
	DisplayAlbumArtist    string        `json:"displayAlbumArtist,omitempty" xml:"displayAlbumArtist,attr,omitempty"`
	DisplayComposer       string        `json:"displayComposer" xml:"displayComposer,attr"`
	ExplicitStatus        string        `json:"explicitStatus" xml:"explicitStatus,attr"`
	Genres                []ItemGenre   `json:"genres,omitempty" xml:"genres"`
	Artists               []ArtistRef   `json:"artists,omitempty" xml:"artists"`
	AlbumArtists          []ArtistRef   `json:"albumArtists,omitempty" xml:"albumArtists"`
	ISRC                  []string      `json:"isrc" xml:"isrc"`
	Moods                 []string      `json:"moods" xml:"moods"`
	Contributors          []Contributor `json:"contributors" xml:"contributors"`
	ReplayGain            *ReplayGain   `json:"replayGain,omitempty" xml:"replayGain,omitempty"`
}

// ArtistID3 is the one artist with its album count.
type ArtistID3 struct {
	ID         string `json:"id" xml:"id,attr"`
	Name       string `json:"name" xml:"name,attr"`
	AlbumCount int    `json:"albumCount" xml:"albumCount,attr"`
	CoverArt   string `json:"coverArt,omitempty" xml:"coverArt,attr,omitempty"`
	SortName   string `json:"sortName,omitempty" xml:"sortName,attr,omitempty"`
}

// AlbumID3 is a lineage.
type AlbumID3 struct {
	ID             string      `json:"id" xml:"id,attr"`
	Name           string      `json:"name" xml:"name,attr"`
	Artist         string      `json:"artist,omitempty" xml:"artist,attr,omitempty"`
	ArtistID       string      `json:"artistId,omitempty" xml:"artistId,attr,omitempty"`
	CoverArt       string      `json:"coverArt,omitempty" xml:"coverArt,attr,omitempty"`
	SongCount      int         `json:"songCount" xml:"songCount,attr"`
	Duration       int         `json:"duration" xml:"duration,attr"`
	PlayCount      int64       `json:"playCount,omitempty" xml:"playCount,attr,omitempty"`
	Played         string      `json:"played,omitempty" xml:"played,attr,omitempty"`
	Created        string      `json:"created" xml:"created,attr"`
	Starred        string      `json:"starred,omitempty" xml:"starred,attr,omitempty"`
	Year           int         `json:"year,omitempty" xml:"year,attr,omitempty"`
	Genre          string      `json:"genre,omitempty" xml:"genre,attr,omitempty"`
	UserRating     int         `json:"userRating,omitempty" xml:"userRating,attr,omitempty"`
	SortName       string      `json:"sortName,omitempty" xml:"sortName,attr,omitempty"`
	MusicBrainzID  string      `json:"musicBrainzId" xml:"musicBrainzId,attr"`
	DisplayArtist  string      `json:"displayArtist,omitempty" xml:"displayArtist,attr,omitempty"`
	IsCompilation  bool        `json:"isCompilation" xml:"isCompilation,attr"`
	ExplicitStatus string      `json:"explicitStatus" xml:"explicitStatus,attr"`
	Genres         []ItemGenre `json:"genres,omitempty" xml:"genres"`
	Artists        []ArtistRef `json:"artists,omitempty" xml:"artists"`
	ReleaseTypes   []string    `json:"releaseTypes" xml:"releaseTypes"`
	Moods          []string    `json:"moods" xml:"moods"`
	DiscTitles     []DiscTitle `json:"discTitles" xml:"discTitles"`
}

// AlbumWithSong is AlbumID3 plus its songs (getAlbum).
type AlbumWithSong struct {
	AlbumID3
	Song []Child `json:"song" xml:"song"`
}

// ArtistWithAlb is the artist plus its albums (getArtist).
type ArtistWithAlb struct {
	ArtistID3
	Album []AlbumID3 `json:"album" xml:"album"`
}

// Artist is an artist entry of getIndexes and searchResult2.
type Artist struct {
	ID   string `json:"id" xml:"id,attr"`
	Name string `json:"name" xml:"name,attr"`
}

// Index is one letter group of getIndexes.
type Index struct {
	Name   string   `json:"name" xml:"name,attr"`
	Artist []Artist `json:"artist" xml:"artist"`
}

// Indexes is the answer of getIndexes.
type Indexes struct {
	LastModified    int64   `json:"lastModified" xml:"lastModified,attr"`
	IgnoredArticles string  `json:"ignoredArticles" xml:"ignoredArticles,attr"`
	Index           []Index `json:"index,omitempty" xml:"index"`
}

// Directory is the answer of getMusicDirectory.
type Directory struct {
	ID     string  `json:"id" xml:"id,attr"`
	Name   string  `json:"name" xml:"name,attr"`
	Parent string  `json:"parent,omitempty" xml:"parent,attr,omitempty"`
	Child  []Child `json:"child" xml:"child"`
}

// IndexID3 is one letter group of getArtists.
type IndexID3 struct {
	Name   string      `json:"name" xml:"name,attr"`
	Artist []ArtistID3 `json:"artist" xml:"artist"`
}

// ArtistsID3 is the answer of getArtists.
type ArtistsID3 struct {
	IgnoredArticles string     `json:"ignoredArticles" xml:"ignoredArticles,attr"`
	Index           []IndexID3 `json:"index,omitempty" xml:"index"`
}

// Genre is one entry of getGenres.
type Genre struct {
	Value      string `json:"value" xml:",chardata"`
	SongCount  int    `json:"songCount" xml:"songCount,attr"`
	AlbumCount int    `json:"albumCount" xml:"albumCount,attr"`
}

// Genres is the answer of getGenres.
type Genres struct {
	Genre []Genre `json:"genre" xml:"genre"`
}

// AlbumList is the answer of getAlbumList (albums as directories).
type AlbumList struct {
	Album []Child `json:"album" xml:"album"`
}

// AlbumList2 is the answer of getAlbumList2.
type AlbumList2 struct {
	Album []AlbumID3 `json:"album" xml:"album"`
}

// SongList is a plain list of songs (getRandomSongs, getSongsByGenre, getSimilarSongs*, getTopSongs).
type SongList struct {
	Song []Child `json:"song" xml:"song"`
}

// Starred is the answer of getStarred.
type Starred struct {
	Song  []Child `json:"song" xml:"song"`
	Album []Child `json:"album" xml:"album"`
}

// Starred2 is the answer of getStarred2.
type Starred2 struct {
	Song  []Child    `json:"song" xml:"song"`
	Album []AlbumID3 `json:"album" xml:"album"`
}

// NowPlaying is the (always empty) answer of getNowPlaying.
type NowPlaying struct {
	Entry []Child `json:"entry" xml:"entry"`
}

// ScanStatus is the answer of getScanStatus.
type ScanStatus struct {
	Scanning bool `json:"scanning" xml:"scanning,attr"`
	Count    int  `json:"count" xml:"count,attr"`
}

// ArtistInfo is the (empty) answer of getArtistInfo and getArtistInfo2.
type ArtistInfo struct{}

// AlbumInfo is the (empty) answer of getAlbumInfo and getAlbumInfo2.
type AlbumInfo struct{}

// SearchResult2 is the answer of search2.
type SearchResult2 struct {
	Artist []Artist `json:"artist,omitempty" xml:"artist"`
	Album  []Child  `json:"album,omitempty" xml:"album"`
	Song   []Child  `json:"song,omitempty" xml:"song"`
}

// SearchResult3 is the answer of search3.
type SearchResult3 struct {
	Artist []ArtistID3 `json:"artist,omitempty" xml:"artist"`
	Album  []AlbumID3  `json:"album,omitempty" xml:"album"`
	Song   []Child     `json:"song,omitempty" xml:"song"`
}
