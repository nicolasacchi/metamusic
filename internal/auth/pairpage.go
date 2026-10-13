package auth

import (
	_ "embed"
	"fmt"
	"html"
	"net/http"
)

//go:embed pair.js
var pairJS []byte

const pageShell = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="referrer" content="no-referrer"><title>metamusic - %s</title></head>
<body><main><h1>metamusic</h1>%s</main></body></html>`

// securePage sets the headers every public pairing page carries (C26).
func securePage(w http.ResponseWriter, contentType string) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
}

// writePage renders a fixed body (never user input) inside the shell.
func writePage(w http.ResponseWriter, status int, title, body string) {
	securePage(w, "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, pageShell, html.EscapeString(title), body)
}

const chooserBody = `%s
<form method="post" action="/start"><button type="submit">Continue with sign-in</button></form>
<p><a href="/pair">Pair this device</a></p>`

const pairFormBody = `%s
<p>Enter the pairing code shown on your computer.</p>
<form method="post" action="/auth/pair">
<label>Pairing code <input id="code" name="code" autocomplete="one-time-code" inputmode="text" maxlength="9" autocapitalize="characters" spellcheck="false" required autofocus></label>
<button type="submit">Pair</button></form>
<script src="/pair.js" defer></script>`

const expiredNotice = `<p role="status">This device's session ended. Sign in again or pair this device again.</p>`
const invalidNotice = `<p role="alert">Code not valid or expired</p>`

// sprintfBody inserts a trusted, fixed notice into a page body.
func sprintfBody(tmpl, notice string) string { return fmt.Sprintf(tmpl, notice) }
