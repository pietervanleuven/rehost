package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"charm.land/lipgloss/v2"

	"github.com/placeholder/rehost/internal/detect"
	"github.com/placeholder/rehost/internal/inventory"
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
		fmt.Fprintf(r.out, "%s %s\n", roleStyle.Render(hr.Role+":"), titleStyle.Render(hr.Caps.Target()))
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
		fmt.Fprintf(r.out, "  %s\n", dimStyle.Render("frameworks:"))
		if len(hr.Installs) == 0 {
			fmt.Fprintf(r.out, "    %s\n", dimStyle.Render("none detected"))
		}
		for _, inst := range hr.Installs {
			label, detail := formatInstall(inst)
			fmt.Fprintf(r.out, "    %s %s\n", okStyle.Render(label), dimStyle.Render(detail))
			for _, line := range inventoryLines(hr.Inventories[inst.Root]) {
				fmt.Fprintf(r.out, "      %s\n", dimStyle.Render(line))
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
		fmt.Fprintf(w, "%s: %s\n", hr.Role, hr.Caps.Target())
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
		if len(hr.Installs) == 0 {
			fmt.Fprintf(w, "  framework:\tnone detected\t\n")
		}
		for _, inst := range hr.Installs {
			label, detail := formatInstall(inst)
			fmt.Fprintf(w, "  framework:\t%s\t%s\n", label, detail)
			for _, line := range inventoryLines(hr.Inventories[inst.Root]) {
				fmt.Fprintf(w, "  \t\t%s\n", line)
			}
		}
	}
	return w.Flush()
}

// inventoryLines renders an install's size picture: the total with the
// largest directories, and the framework caches/backups worth excluding.
func inventoryLines(inv *inventory.Inventory) []string {
	if inv == nil || inv.TotalKB == 0 {
		return nil
	}
	line := inventory.HumanKB(inv.TotalKB)
	if len(inv.Top) > 0 {
		var parts []string
		for i, e := range inv.Top {
			if i == 3 {
				break
			}
			parts = append(parts, fmt.Sprintf("%s %s", e.Path, inventory.HumanKB(e.SizeKB)))
		}
		line += " · largest: " + strings.Join(parts, ", ")
	}
	lines := []string{line}
	if len(inv.Suggested) > 0 {
		var parts []string
		for _, e := range inv.Suggested {
			parts = append(parts, fmt.Sprintf("%s %s", e.Path, inventory.HumanKB(e.SizeKB)))
		}
		lines = append(lines, "suggested excludes: "+strings.Join(parts, ", "))
	}
	return lines
}

// formatInstall renders one install as a label ("wordpress 6.5.2") and a
// detail string (root plus multisite/config notes).
func formatInstall(inst detect.Install) (label, detail string) {
	label = inst.Framework
	if inst.Version != "" {
		label += " " + inst.Version
	}
	detail = inst.Root
	if len(inst.Sites) > 1 {
		detail += " · multisite: " + strings.Join(inst.Sites, ", ")
	}
	if inst.ConfigFile != "" {
		detail += " · config " + inst.ConfigFile
	}
	return label, detail
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
	Installs    []detect.Install                `json:"installs"`
	Inventories map[string]*inventory.Inventory `json:"inventories,omitempty"`
}

// Envelope is the versioned JSON output shape of the capability report.
type Envelope struct {
	Schema string     `json:"schema"`
	Hosts  []jsonHost `json:"hosts"`
}

func (r jsonRenderer) CapabilityReport(reports []HostReport) error {
	env := Envelope{Schema: reportSchema}
	for _, hr := range reports {
		installs := hr.Installs
		if installs == nil {
			installs = []detect.Install{}
		}
		env.Hosts = append(env.Hosts, jsonHost{Role: hr.Role, Capabilities: hr.Caps, Installs: installs, Inventories: hr.Inventories})
	}
	enc := json.NewEncoder(r.out)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}

func (r jsonRenderer) Error(err error) {
	enc := json.NewEncoder(r.out)
	_ = enc.Encode(map[string]string{"schema": "rehost.error.v1", "error": err.Error()})
}
