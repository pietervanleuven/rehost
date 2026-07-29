package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/pietervanleuven/rehost/internal/ssh"
	"github.com/pietervanleuven/rehost/internal/tui"
)

// ui bundles the per-command presentation state every host-touching command
// needs: the resolved output mode and everything that follows from it.
type ui struct {
	mode        tui.Mode
	renderer    tui.Renderer
	interactive bool // prompts (passwords, TOFU, forms) allowed
	prompter    ssh.Prompter
	progress    func(format string, a ...any)
}

// newUI resolves the output mode once and derives the rest: prompts only
// exist on an interactive terminal, progress goes to stderr so the report on
// stdout stays clean, and JSON mode stays silent so nothing but the document
// reaches a consumer.
func newUI(cmd *cobra.Command, opts *options) ui {
	mode := opts.outputMode(cmd.OutOrStdout())
	u := ui{
		mode:        mode,
		renderer:    tui.New(mode, cmd.OutOrStdout()),
		interactive: mode == tui.ModeStyled,
	}
	if u.interactive {
		u.prompter = tui.HuhPrompter{}
	} else {
		u.prompter = tui.NonInteractivePrompter{}
	}
	u.progress = func(format string, a ...any) {
		if mode != tui.ModeJSON {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), format+"\n", a...)
		}
	}
	return u
}

// fail routes err through the renderer in JSON mode — stdout stays a
// machine-readable document even on failure — and returns it unchanged, so
// call sites read `return u.fail(err)`.
func (u ui) fail(err error) error {
	if u.mode == tui.ModeJSON {
		u.renderer.Error(err)
	}
	return err
}

// outputMode picks the renderer: --json wins, then plain for non-TTY or
// suppressed color, styled otherwise. The TTY test runs against the stream
// the command actually writes to, so a redirected cobra out (tests, future
// piping seams) is honored instead of the process-global stdout.
func (o *options) outputMode(out io.Writer) tui.Mode {
	if o.json {
		return tui.ModeJSON
	}
	if o.noColor || os.Getenv("NO_COLOR") != "" {
		return tui.ModePlain
	}
	if f, ok := out.(*os.File); !ok || !term.IsTerminal(int(f.Fd())) {
		return tui.ModePlain
	}
	return tui.ModeStyled
}
