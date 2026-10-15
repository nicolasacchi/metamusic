// Field names and semantics follow the OpenSubsonic API description
// (third_party/opensubsonic/openapi.json, commit be131fb44a48fb346faa16f5a242785fdfe3052b, Apache-2.0,
// © OpenSubsonic contributors). Written for this project from that description; no code from GPL-licensed
// Subsonic servers or client libraries.

package subsonic

import "encoding/xml"

// APIVersion is the Subsonic REST version this server speaks.
const APIVersion = "1.16.1"

// xmlNamespace is the namespace of every XML response.
const xmlNamespace = "http://subsonic.org/restapi"

// ErrorBody is the error element of a failed response.
type ErrorBody struct {
	Code    int    `json:"code" xml:"code,attr"`
	Message string `json:"message" xml:"message,attr"`
}

// Response is the subsonic-response envelope (C108.4). Endpoint payloads join it in later tasks (S1-S3).
type Response struct {
	XMLName       xml.Name   `json:"-" xml:"subsonic-response"`
	XMLNS         string     `json:"-" xml:"xmlns,attr"`
	Status        string     `json:"status" xml:"status,attr"`
	Version       string     `json:"version" xml:"version,attr"`
	Type          string     `json:"type" xml:"type,attr"`
	ServerVersion string     `json:"serverVersion" xml:"serverVersion,attr"`
	OpenSubsonic  bool       `json:"openSubsonic" xml:"openSubsonic,attr"`
	Error         *ErrorBody `json:"error,omitempty" xml:"error,omitempty"`

	// System endpoints (S1).
	License                *License        `json:"license,omitempty" xml:"license,omitempty"`
	OpenSubsonicExtensions []ExtensionInfo `json:"openSubsonicExtensions,omitempty" xml:"openSubsonicExtensions"`
	TokenInfo              *TokenInfo      `json:"tokenInfo,omitempty" xml:"tokenInfo,omitempty"`
	User                   *User           `json:"user,omitempty" xml:"user,omitempty"`
	MusicFolders           *MusicFolders   `json:"musicFolders,omitempty" xml:"musicFolders,omitempty"`
}

// License is the answer of getLicense.
type License struct {
	Valid bool `json:"valid" xml:"valid,attr"`
}

// ExtensionInfo is one advertised OpenSubsonic extension.
type ExtensionInfo struct {
	Name     string `json:"name" xml:"name,attr"`
	Versions []int  `json:"versions" xml:"versions"`
}

// TokenInfo is the answer of tokenInfo.
type TokenInfo struct {
	Username string `json:"username" xml:"username,attr"`
}

// User is the answer of getUser.
type User struct {
	Username            string `json:"username" xml:"username,attr"`
	ScrobblingEnabled   bool   `json:"scrobblingEnabled" xml:"scrobblingEnabled,attr"`
	AdminRole           bool   `json:"adminRole" xml:"adminRole,attr"`
	SettingsRole        bool   `json:"settingsRole" xml:"settingsRole,attr"`
	DownloadRole        bool   `json:"downloadRole" xml:"downloadRole,attr"`
	UploadRole          bool   `json:"uploadRole" xml:"uploadRole,attr"`
	PlaylistRole        bool   `json:"playlistRole" xml:"playlistRole,attr"`
	CoverArtRole        bool   `json:"coverArtRole" xml:"coverArtRole,attr"`
	CommentRole         bool   `json:"commentRole" xml:"commentRole,attr"`
	PodcastRole         bool   `json:"podcastRole" xml:"podcastRole,attr"`
	StreamRole          bool   `json:"streamRole" xml:"streamRole,attr"`
	JukeboxRole         bool   `json:"jukeboxRole" xml:"jukeboxRole,attr"`
	ShareRole           bool   `json:"shareRole" xml:"shareRole,attr"`
	VideoConversionRole bool   `json:"videoConversionRole" xml:"videoConversionRole,attr"`
	Folder              []int  `json:"folder" xml:"folder"`
}

// MusicFolder is one of the three virtual folders (D-M4a-10).
type MusicFolder struct {
	ID   int    `json:"id" xml:"id,attr"`
	Name string `json:"name" xml:"name,attr"`
}

// MusicFolders is the answer of getMusicFolders.
type MusicFolders struct {
	MusicFolder []MusicFolder `json:"musicFolder" xml:"musicFolder"`
}
