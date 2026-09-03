package httpapi

import (
	"net/http/httptest"
	"testing"
)

func TestInlineImageContentTypeAllowsOnlyRasterImages(t *testing.T) {
	cases := []struct {
		name   string
		stored string
		want   string
		ok     bool
	}{
		{name: "png", stored: "image/png", want: "image/png", ok: true},
		{name: "uppercase is normalized", stored: "IMAGE/PNG", want: "image/png", ok: true},
		{name: "jpg folds onto jpeg", stored: "image/jpg", want: "image/jpeg", ok: true},
		{name: "parameters are dropped", stored: "image/gif; charset=utf-8", want: "image/gif", ok: true},
		{name: "webp", stored: "image/webp", want: "image/webp", ok: true},
		{name: "svg is scriptable", stored: "image/svg+xml"},
		{name: "svg with charset", stored: "image/svg+xml; charset=utf-8"},
		{name: "html", stored: "text/html"},
		{name: "html smuggled behind an image prefix", stored: "image/png, text/html"},
		{name: "unparseable value keeps the type token", stored: "image/png; =", want: "image/png", ok: true},
		{name: "unparseable non-image", stored: "text/html; =", want: "", ok: false},
		{name: "empty", stored: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := inlineImageContentType(tc.stored)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("inlineImageContentType(%q) = (%q, %v), want (%q, %v)", tc.stored, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestContentDispositionAttachmentEscapesUploadNames(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain ascii stays verbatim",
			in:   "report.pdf",
			want: `attachment; filename="report.pdf"`,
		},
		{
			name: "quotes cannot close the quoted-string",
			in:   `a";filename="evil.html`,
			want: `attachment; filename="a_;filename=_evil.html"; filename*=UTF-8''a%22%3Bfilename%3D%22evil.html`,
		},
		{
			name: "control characters are replaced",
			in:   "line\r\nbreak.png",
			want: `attachment; filename="line__break.png"; filename*=UTF-8''line%0D%0Abreak.png`,
		},
		{
			name: "non-ascii gets the extended form",
			in:   "보고서.png",
			want: `attachment; filename="___.png"; filename*=UTF-8''%EB%B3%B4%EA%B3%A0%EC%84%9C.png`,
		},
		{
			name: "empty name falls back",
			in:   "   ",
			want: `attachment; filename="download"`,
		},
		{
			name: "all-unprintable name falls back",
			in:   "\x01\x02",
			want: `attachment; filename="download"; filename*=UTF-8''%01%02`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := contentDispositionAttachment(tc.in); got != tc.want {
				t.Fatalf("contentDispositionAttachment(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestWriteInlineImageHeadersForcesUnsafeTypesDown(t *testing.T) {
	inline := httptest.NewRecorder()
	writeInlineImageHeaders(inline, "image/jpg", "photo.jpg")
	if got := inline.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Fatalf("inline Content-Type = %q, want image/jpeg", got)
	}
	if got := inline.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("inline X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := inline.Header().Get("Content-Disposition"); got != "" {
		t.Fatalf("inline Content-Disposition = %q, want none", got)
	}

	scriptable := httptest.NewRecorder()
	writeInlineImageHeaders(scriptable, "image/svg+xml", "payload.svg")
	if got := scriptable.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("svg Content-Type = %q, want application/octet-stream", got)
	}
	if got := scriptable.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("svg X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := scriptable.Header().Get("Content-Disposition"); got != `attachment; filename="payload.svg"` {
		t.Fatalf("svg Content-Disposition = %q, want an attachment", got)
	}
}
