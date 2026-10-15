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
}
