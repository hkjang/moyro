package email

import (
	"bytes"
	"fmt"
	"html/template"
	txtTemplate "text/template"
)

// DigestMention carries one @-mention for the digest template. Username is
// the mentioning user; ChannelName + Excerpt give the recipient enough
// context to decide whether to dive into the full thread.
type DigestMention struct {
	Username    string
	ChannelName string
	Excerpt     string
	PostedAt    string // pre-formatted local time, e.g. "오후 3:14"
}

// DigestData is the render payload.
type DigestData struct {
	Recipient string // display name or username
	BaseURL   string // e.g. "http://localhost:8065" — for CTA link
	Mentions  []DigestMention
}

const digestHTML = `<!doctype html>
<html lang="ko"><body style="font-family:sans-serif;max-width:560px;margin:0 auto;padding:16px;">
<h2>안녕하세요, {{.Recipient}}님.</h2>
<p>회의가 없는 동안 아래 멘션이 도착했습니다.</p>
{{range .Mentions}}
<div style="border-left:4px solid #4c6ef5;padding:8px 12px;margin:12px 0;background:#f8f9fa;">
  <div><strong>{{.Username}}</strong> · #{{.ChannelName}} · {{.PostedAt}}</div>
  <div style="margin-top:4px;color:#333;">{{.Excerpt}}</div>
</div>
{{end}}
<p style="margin-top:24px;"><a href="{{.BaseURL}}" style="background:#4c6ef5;color:#fff;padding:10px 16px;border-radius:4px;text-decoration:none;">moyro 열기</a></p>
<hr/>
<p style="color:#868e96;font-size:12px;">이메일 수신을 원치 않으시면 프로필 설정에서 해제할 수 있습니다.</p>
</body></html>`

const digestText = `안녕하세요, {{.Recipient}}님.

회의가 없는 동안 아래 멘션이 도착했습니다.

{{range .Mentions}}- {{.Username}} (#{{.ChannelName}}, {{.PostedAt}}): {{.Excerpt}}
{{end}}

moyro 열기: {{.BaseURL}}

이메일 수신을 원치 않으시면 프로필 설정에서 해제할 수 있습니다.
`

var (
	htmlTpl = template.Must(template.New("digest_html").Parse(digestHTML))
	textTpl = txtTemplate.Must(txtTemplate.New("digest_text").Parse(digestText))
)

// RenderDigest returns (subject, html, text) for the given data.
func RenderDigest(d DigestData) (string, string, string, error) {
	var h, t bytes.Buffer
	if err := htmlTpl.Execute(&h, d); err != nil {
		return "", "", "", err
	}
	if err := textTpl.Execute(&t, d); err != nil {
		return "", "", "", err
	}
	subject := fmt.Sprintf("[moyro] 멘션 %d건이 기다리고 있어요", len(d.Mentions))
	return subject, h.String(), t.String(), nil
}
