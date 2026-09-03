// User-uploaded bytes are served from the same origin as the webapp, so the
// Content-Type we echo back is what decides whether a browser treats them as
// a document. Upload only records the MIME the client declared in its
// multipart part header — nothing inspects the bytes — which means any member
// could store `image/svg+xml` (an XML document format that carries <script>
// and event handlers) and hand every viewer a same-origin script by linking
// the file URL. The API responses carry no Content-Security-Policy either;
// that header is set by the webui handler for the SPA document only, and a
// direct navigation to /api/v4/files/{id}/preview never passes through it.
//
// These helpers keep every byte-serving route on one policy: nosniff always,
// an inline render only for the raster image types that have no scripting
// surface, and anything else forced down as an attachment.
package httpapi

import (
	"mime"
	"net/http"
	"strconv"
	"strings"
)

// inlineImageTypes is the allowlist of types we are willing to render inline.
// SVG is deliberately absent, as is anything text/* or application/*.
var inlineImageTypes = map[string]struct{}{
	"image/png":  {},
	"image/jpeg": {},
	"image/gif":  {},
	"image/webp": {},
	"image/bmp":  {},
	"image/avif": {},
}

// inlineImageContentType normalizes a stored MIME type and reports whether it
// is safe to hand a browser inline. Parameters (`; charset=…`) are dropped so
// a crafted suffix can't slip past the allowlist, and the unregistered
// `image/jpg` spelling some clients still send is folded onto `image/jpeg`.
func inlineImageContentType(stored string) (string, bool) {
	parsed, _, err := mime.ParseMediaType(strings.TrimSpace(stored))
	if err != nil {
		// Unparseable values still get a best-effort read of the type token so
		// a stray parameter doesn't turn a known-good image into a download.
		parsed = strings.ToLower(strings.TrimSpace(strings.SplitN(stored, ";", 2)[0]))
	}
	if parsed == "image/jpg" {
		parsed = "image/jpeg"
	}
	if _, ok := inlineImageTypes[parsed]; !ok {
		return "", false
	}
	return parsed, true
}

// writeInlineImageHeaders sets the response headers for user-supplied bytes
// that the caller wants rendered in place. Allowlisted types are served under
// their normalized name; everything else becomes an opaque download so the
// browser never parses it as a document. downloadName is only consulted on the
// attachment path.
func writeInlineImageHeaders(w http.ResponseWriter, storedMIME, downloadName string) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if contentType, ok := inlineImageContentType(storedMIME); ok {
		w.Header().Set("Content-Type", contentType)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", contentDispositionAttachment(downloadName))
}

// contentDispositionAttachment builds an RFC 6266 attachment header for an
// upload's stored name. files.sanitize only strips path separators, so the
// name can still hold a quote or a control character; interpolating those
// straight into a quoted-string would let the uploader close the string early
// and append parameters of their own.
func contentDispositionAttachment(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "download"
	}
	ascii := asciiAttachmentName(name)
	header := `attachment; filename="` + ascii + `"`
	if ascii != name {
		// The name lost information (non-ASCII, quotes, controls). Offer the
		// exact bytes through the extended form for clients that understand it.
		header += "; filename*=UTF-8''" + rfc5987Escape(name)
	}
	return header
}

// asciiAttachmentName reduces a name to the printable ASCII a quoted-string
// can hold verbatim, replacing everything else with an underscore.
func asciiAttachmentName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if strings.Trim(out, "_. ") == "" {
		return "download"
	}
	return out
}

// rfc5987UnreservedChars are the attr-char bytes RFC 5987 lets an ext-value
// carry literally; everything else is percent-encoded.
const rfc5987UnreservedChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!#$&+-.^_`|~"

func rfc5987Escape(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	for i := 0; i < len(value); i++ {
		c := value[i]
		if strings.IndexByte(rfc5987UnreservedChars, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		hex := strings.ToUpper(strconv.FormatUint(uint64(c), 16))
		if len(hex) == 1 {
			b.WriteByte('0')
		}
		b.WriteString(hex)
	}
	return b.String()
}
