package server

import (
	"bytes"
	"html/template"
	"strings"

	"github.com/kibanbuild/dojo/internal/server/repo"
)

// welcomeData is what E1 says: who it is for, the path they are on and its first role (empty when the
// catalog has none), their GitHub login for the practice repo, and where Dojo lives.
type welcomeData struct {
	Username    string
	GithubLogin string
	PathTitle   string
	RoleTitle   string
	Link        string
	InstallCmd  string
}

const welcomeSubject = "Welcome to Dojo"

// welcomeInstallCmd is the real install line for an origin (design 3.9).
func welcomeInstallCmd(origin string) string { return "curl -fsSL " + origin + "/install | sh" }

// welcomePath picks the path E1 names: the learner's chosen one when it is available, else the first
// available one, with the title of its first role. Either title is empty when the catalog has none.
func welcomePath(paths []repo.ListCatalogPathsRow, chosen string, pathRoles []repo.ListCatalogPathRolesRow, roles []repo.ListCatalogRolesRow) (pathTitle, roleTitle string) {
	var pick *repo.ListCatalogPathsRow
	for i := range paths {
		if paths[i].Status != "available" {
			continue
		}
		if pick == nil {
			pick = &paths[i]
		}
		if chosen != "" && paths[i].Slug == chosen {
			pick = &paths[i]
			break
		}
	}
	if pick == nil {
		return "", ""
	}
	first, best := "", int32(0)
	for _, pr := range pathRoles {
		if pr.Path == pick.Slug && (first == "" || pr.Position < best) {
			first, best = pr.Role, pr.Position
		}
	}
	for _, r := range roles {
		if r.Slug == first {
			return pick.Title, r.Title
		}
	}
	return pick.Title, ""
}

// welcomeText is the plain-text part (every email has one, design accessibility section).
func welcomeText(d welcomeData) string {
	var b strings.Builder
	b.WriteString("Hi " + d.Username + ",\n\n")
	if d.PathTitle != "" {
		b.WriteString("Welcome to Dojo. Your account is ready, and you are on the " + d.PathTitle + " path.")
		if d.RoleTitle != "" {
			b.WriteString(" Your first role is " + d.RoleTitle + ".")
		}
		b.WriteString("\n\n")
	} else {
		b.WriteString("Welcome to Dojo. Your account is ready.\n\n")
	}
	b.WriteString("Each ticket is a small, realistic problem that you solve on a lab machine, the way you would at work. " +
		"Start with the first one whenever you have half an hour.\n\n")
	b.WriteString("Three things to do next:\n\n")
	b.WriteString("1. Install the Dojo command line: " + d.InstallCmd + "\n")
	b.WriteString("2. Sign in from your terminal: dojo login\n")
	b.WriteString("3. Check your machine: dojo doctor\n\n")
	b.WriteString("Labs run on your computer, and the work stays in your own " + d.GithubLogin + "/dojo repo.\n\n")
	b.WriteString("Open Dojo: " + d.Link + "\n\n")
	b.WriteString("If you did not sign up, ignore this message.\n\n")
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
{{if .PathTitle}}<p style="margin:0 0 16px;">Welcome to Dojo. Your account is ready, and you are on the {{.PathTitle}} path.{{if .RoleTitle}} Your first role is {{.RoleTitle}}.{{end}}</p>
{{else}}<p style="margin:0 0 16px;">Welcome to Dojo. Your account is ready.</p>
{{end}}<p style="margin:0 0 16px;">Each ticket is a small, realistic problem that you solve on a lab machine, the way you would at work. Start with the first one whenever you have half an hour.</p>
<p style="margin:0 0 8px;">Three things to do next:</p>
<ol style="margin:0 0 16px;padding-left:24px;">
<li>Install the Dojo command line: <code style="font-family:Menlo,Consolas,monospace;font-size:15px;">{{.InstallCmd}}</code></li>
<li>Sign in from your terminal: <code style="font-family:Menlo,Consolas,monospace;font-size:15px;">dojo login</code></li>
<li>Check your machine: <code style="font-family:Menlo,Consolas,monospace;font-size:15px;">dojo doctor</code></li>
</ol>
<p style="margin:0 0 16px;">Labs run on your computer, and the work stays in your own {{.GithubLogin}}/dojo repo.</p>
<p style="margin:0 0 16px;"><a href="{{.Link}}" style="color:#4c1d95;text-decoration:underline;">Open Dojo</a></p>
<p style="margin:0 0 16px;">If you did not sign up, ignore this message.</p>
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
