package server

import (
	"bytes"
	"html/template"
	"strings"
)

// welcomeData is what E1 says: who it is for, the path they are on (empty when the catalog has none)
// and where Dojo lives.
type welcomeData struct {
	Username  string
	PathTitle string
	Link      string
}

const welcomeSubject = "Welcome to Dojo"

// welcomeText is the plain-text part (every email has one, design accessibility section).
func welcomeText(d welcomeData) string {
	var b strings.Builder
	b.WriteString("Hi " + d.Username + ",\n\n")
	if d.PathTitle != "" {
		b.WriteString("Welcome to Dojo. Your account is ready, and you are on the " + d.PathTitle + " path.\n\n")
	} else {
		b.WriteString("Welcome to Dojo. Your account is ready.\n\n")
	}
	b.WriteString("Each ticket is a small, realistic problem that you solve on a lab machine, the way you would at work. " +
		"Start with the first one whenever you have half an hour.\n\n")
	b.WriteString("Open Dojo: " + d.Link + "\n\n")
	b.WriteString("Carmen\n")
	return b.String()
}

// welcomeHTML is a real table layout, 16 px body text and an underlined link (design accessibility section).
var welcomeHTML = template.Must(template.New("e1").Parse(`<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Welcome to Dojo</title></head>
<body style="margin:0;padding:0;background:#ffffff;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"><tr><td align="center" style="padding:24px 16px;">
<table role="presentation" width="560" cellpadding="0" cellspacing="0" border="0" style="max-width:560px;width:100%;"><tr><td style="font-family:Arial,Helvetica,sans-serif;font-size:16px;line-height:24px;color:#1a1a1a;">
<p style="margin:0 0 16px;">Hi {{.Username}},</p>
{{if .PathTitle}}<p style="margin:0 0 16px;">Welcome to Dojo. Your account is ready, and you are on the {{.PathTitle}} path.</p>
{{else}}<p style="margin:0 0 16px;">Welcome to Dojo. Your account is ready.</p>
{{end}}<p style="margin:0 0 16px;">Each ticket is a small, realistic problem that you solve on a lab machine, the way you would at work. Start with the first one whenever you have half an hour.</p>
<p style="margin:0 0 16px;"><a href="{{.Link}}" style="color:#4c1d95;text-decoration:underline;">Open Dojo</a></p>
<p style="margin:0;">Carmen</p>
</td></tr></table>
</td></tr></table>
</body>
</html>
`))

func renderWelcomeHTML(d welcomeData) (string, error) {
	var b bytes.Buffer
	if err := welcomeHTML.Execute(&b, d); err != nil {
		return "", err
	}
	return b.String(), nil
}
