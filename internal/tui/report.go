package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"charm.land/lipgloss/v2"

	"github.com/placeholder/rehost/internal/ssh"
)

// reportSchema versions the JSON envelope so later phases can extend it
// without breaking parsers.
const reportSchema = "rehost.capability-report.v1"

// --- styled ---

type styledRenderer struct{ out io.Writer }

var (
	titleStyle   = lipgloss.NewStyle().Bold(true)
	roleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	okStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	missingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	dimStyle     = lipgloss.NewStyle().Faint(true)
	errStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
)

func (r styledRenderer) CapabilityReport(reports []HostReport) error {
	for i, hr := range reports {
		if i > 0 {
			fmt.Fprintln(r.out)
		}
		fmt.Fprintf(r.out, "%s %s\n", roleStyle.Render(hr.Role+":"), titleStyle.Render(hr.Caps.Host))
		fmt.Fprintf(r.out, "  %s\n", dimStyle.Render(summaryLine(hr.Caps)))
		for _, name := range ssh.ProbedTools() {
			tool := hr.Caps.Tools[name]
			if tool.Found {
				detail := tool.Path
				if tool.Version != "" {
					detail += "  " + tool.Version
				}
				fmt.Fprintf(r.out, "  %s %-10s %s\n", okStyle.Render("✓"), name, dimStyle.Render(detail))
			} else {
				fmt.Fprintf(r.out, "  %s %-10s %s\n", missingStyle.Render("✗"), name, dimStyle.Render("not found"))
			}
		}
	}
	return nil
}

func (r styledRenderer) Error(err error) {
	fmt.Fprintf(r.out, "%s %v\n", errStyle.Render("Error:"), err)
}

// --- plain (non-TTY / CI) ---

type plainRenderer struct{ out io.Writer }

func (r plainRenderer) CapabilityReport(reports []HostReport) error {
	w := tabwriter.NewWriter(r.out, 2, 4, 2, ' ', 0)
	for i, hr := range reports {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "%s: %s\n", hr.Role, hr.Caps.Host)
		fmt.Fprintf(w, "  %s\n", summaryLine(hr.Caps))
		for _, name := range ssh.ProbedTools() {
			tool := hr.Caps.Tools[name]
			if tool.Found {
				detail := tool.Path
				if tool.Version != "" {
					detail += "  " + tool.Version
				}
				fmt.Fprintf(w, "  [ok]\t%s\t%s\n", name, detail)
			} else {
				fmt.Fprintf(w, "  [missing]\t%s\t\n", name)
			}
		}
	}
	return w.Flush()
}

func (r plainRenderer) Error(err error) {
	fmt.Fprintf(r.out, "error: %v\n", err)
}

// summaryLine condenses host facts into one line under the host header.
func summaryLine(caps *ssh.Capabilities) string {
	s := "shell " + orUnknown(caps.Shell)
	if caps.Uname != "" {
		s += " · " + caps.Uname
	}
	if caps.PHPVersion != "" {
		s += " · PHP " + caps.PHPVersion
	} else {
		s += " · PHP not detected"
	}
	if caps.Home != "" {
		s += " · home " + caps.Home
	}
	return s
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// --- JSON ---

type jsonRenderer struct{ out io.Writer }

type jsonHost struct {
	Role string `json:"role"`
	*ssh.Capabilities
}

// Envelope is the versioned JSON output shape of the capability report.
type Envelope struct {
	Schema string     `json:"schema"`
	Hosts  []jsonHost `json:"hosts"`
}

func (r jsonRenderer) CapabilityReport(reports []HostReport) error {
	env := Envelope{Schema: reportSchema}
	for _, hr := range reports {
		env.Hosts = append(env.Hosts, jsonHost{Role: hr.Role, Capabilities: hr.Caps})
	}
	enc := json.NewEncoder(r.out)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}

func (r jsonRenderer) Error(err error) {
	enc := json.NewEncoder(r.out)
	_ = enc.Encode(map[string]string{"schema": "rehost.error.v1", "error": err.Error()})
}
