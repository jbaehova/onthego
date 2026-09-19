package handoff

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
)

type FailedCommand struct {
	Command      string `json:"command"`
	ExitCode     int    `json:"exit_code"`
	EvidencePath string `json:"evidence_path"`
	StartOffset  int64  `json:"start_offset,omitempty"`
	EndOffset    int64  `json:"end_offset,omitempty"`
}

type Input struct {
	Goal          string          `json:"goal"`
	Completed     []string        `json:"completed,omitempty"`
	CurrentState  []string        `json:"current_state,omitempty"`
	Failed        []FailedCommand `json:"failed_commands,omitempty"`
	NextSteps     []string        `json:"next_steps,omitempty"`
	Constraints   []string        `json:"constraints,omitempty"`
	EvidencePaths []string        `json:"evidence_paths,omitempty"`
}

func Render(in Input) []byte {
	var out bytes.Buffer
	out.WriteString("# ONTHEGO handoff\n\n")
	writeText(&out, "Goal", in.Goal)
	writeList(&out, "Completed", in.Completed)
	writeList(&out, "Current state", in.CurrentState)
	if len(in.Failed) > 0 {
		out.WriteString("## Failed commands\n\n")
		for _, f := range in.Failed {
			fmt.Fprintf(&out, "- `%s` exited %d. Evidence: `%s`", oneLine(f.Command), f.ExitCode, f.EvidencePath)
			if f.EndOffset > f.StartOffset {
				fmt.Fprintf(&out, " bytes %d:%d", f.StartOffset, f.EndOffset)
			}
			out.WriteString("\n")
		}
		out.WriteString("\n")
	}
	writeList(&out, "Next steps", in.NextSteps)
	writeList(&out, "Constraints", in.Constraints)
	evidence := append([]string(nil), in.EvidencePaths...)
	sort.Strings(evidence)
	writeList(&out, "Evidence paths", evidence)
	out.WriteString("This is a context handoff to a new agent run. It does not resume process memory or hidden model state.\n")
	return out.Bytes()
}

func writeText(out *bytes.Buffer, title, value string) {
	out.WriteString("## " + title + "\n\n")
	if strings.TrimSpace(value) == "" {
		out.WriteString("Not provided.\n\n")
		return
	}
	out.WriteString(strings.TrimSpace(value) + "\n\n")
}

func writeList(out *bytes.Buffer, title string, values []string) {
	out.WriteString("## " + title + "\n\n")
	if len(values) == 0 {
		out.WriteString("- None recorded.\n\n")
		return
	}
	for _, value := range values {
		fmt.Fprintf(out, "- %s\n", strings.TrimSpace(value))
	}
	out.WriteString("\n")
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
