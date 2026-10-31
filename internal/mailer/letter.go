package mailer

import (
	"bytes"
	"embed"
	htmltemplate "html/template"
	"strings"
	texttemplate "text/template"
)

// Letter is everything the hub sends, in one shape: a title, a few lines, an
// optional block to copy (a subscription link) and an optional button. The
// words live with the act that sends them (internal/billing).
type Letter struct {
	Title string   `json:"title"`
	Lines []string `json:"lines"`
	// Codes are shown as they are, each in a box, to copy (subscription
	// links).
	Codes       []string `json:"codes,omitempty"`
	Action      string   `json:"action,omitempty"`
	ActionLabel string   `json:"action_label,omitempty"`
}

//go:embed templates/letter.html templates/letter.txt
var templatesFS embed.FS

var (
	htmlLetter = htmltemplate.Must(htmltemplate.ParseFS(templatesFS, "templates/letter.html"))
	textLetter = texttemplate.Must(texttemplate.ParseFS(templatesFS, "templates/letter.txt"))
)

// Render is the letter as a message to to, from sender.
func (l Letter) Render(sender Sender, to string) (Message, error) {
	var html, text bytes.Buffer
	if err := htmlLetter.Execute(&html, l); err != nil {
		return Message{}, err
	}
	if err := textLetter.Execute(&text, l); err != nil {
		return Message{}, err
	}
	return Message{
		From:     sender.From,
		FromName: sender.FromName,
		To:       to,
		Subject:  l.Title,
		TextBody: strings.TrimSpace(text.String()) + "\n",
		HTMLBody: html.String(),
	}, nil
}
