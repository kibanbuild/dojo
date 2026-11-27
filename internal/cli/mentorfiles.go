package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/kibanyadev/dojo/internal/mentor"
	"github.com/kibanyadev/dojo/internal/state"
)

// seedMentorFiles writes AGENTS.md and CLAUDE.md into the local practice clone when it exists and
// the file is absent. It never overwrites and never stages: the files stay local. Failures only warn.
// The clone is the one the pass commit uses (resolveClone).
func (a *App) seedMentorFiles(st *state.State) {
	dir, needClone, err := resolveClone(st, st.GithubLogin)
	if err != nil || needClone {
		return
	}
	files := []struct{ name, text string }{{"AGENTS.md", mentor.AgentsMD()}, {"CLAUDE.md", mentor.ClaudeMD}}
	for _, f := range files {
		if _, err := os.Lstat(filepath.Join(dir, f.name)); err == nil {
			return // either file present: the learner's choice stands, write nothing
		}
	}
	var added []string
	for _, f := range files {
		err := writeIfAbsent(filepath.Join(dir, f.name), f.text)
		switch {
		case err == nil:
			added = append(added, f.name)
		case errors.Is(err, fs.ErrExist):
		default:
			fmt.Fprintf(a.Out, "Warning: %s was not written in %s: %v\n", f.name, dir, err)
		}
	}
	if len(added) > 0 {
		fmt.Fprintf(a.Out, "Mentor rules (%s) added to %s. They stay local: dojo never commits them.\n", strings.Join(added, ", "), dir)
	}
}

func writeIfAbsent(path, text string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) //nolint:gosec // G302: a plain text file in the learner's own repo
	if err != nil {
		return err
	}
	if _, err := f.WriteString(text); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
