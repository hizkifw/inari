package hub

import (
	"encoding/base64"
	"strings"
	"unicode/utf8"

	"github.com/hizkifw/inari/internal/acp"
)

// blocks turns attachments into the content blocks kon accepts: images and
// audio as media, text inline so the model reads it with the message, and
// anything else, such as a PDF, as an embedded blob.
func blocks(attachments []Attachment) []acp.ContentBlock {
	var out []acp.ContentBlock
	for _, a := range attachments {
		mime := a.MIME
		if mime == "" {
			mime = "application/octet-stream"
		}
		// Not a file in the session's directory, so not a file: URI; kon
		// mentions any other URI as it is.
		uri := "attachment:" + a.Name
		data := base64.StdEncoding.EncodeToString(a.Data)
		switch {
		case strings.HasPrefix(mime, "image/"):
			out = append(out, acp.ContentBlock{Type: "image", Data: data, MIMEType: mime})
		case strings.HasPrefix(mime, "audio/"):
			out = append(out, acp.ContentBlock{Type: "audio", Data: data, MIMEType: mime})
		case isText(mime) && utf8.Valid(a.Data):
			text := string(a.Data)
			out = append(out, acp.TextBlock(" "), acp.ContentBlock{Type: "resource", Resource: &acp.Resource{URI: uri, MIMEType: mime, Text: &text}})
		default:
			out = append(out, acp.ContentBlock{Type: "resource", Resource: &acp.Resource{URI: uri, MIMEType: mime, Blob: data}})
		}
	}
	return out
}

func isText(mime string) bool {
	mime, _, _ = strings.Cut(mime, ";")
	if strings.HasPrefix(mime, "text/") {
		return true
	}
	switch mime {
	case "application/json", "application/xml", "application/yaml", "application/x-yaml", "application/toml", "application/javascript", "application/x-sh":
		return true
	}
	return false
}
