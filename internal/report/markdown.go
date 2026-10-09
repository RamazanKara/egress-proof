package report

import (
	"fmt"
	"strings"
	"time"

	"github.com/RamazanKara/egress-proof/internal/probe"
)

var markdownEscaper = strings.NewReplacer(
	"&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&#34;", "'", "&#39;",
	"\\", "&#92;", "`", "&#96;", "|", "&#124;", "*", "&#42;", "_", "&#95;",
	"[", "&#91;", "]", "&#93;", "#", "&#35;", "!", "&#33;", "~", "&#126;",
	"\r\n", "<br>", "\r", "<br>", "\n", "<br>",
)

func markdownText(text string) string {
	return markdownEscaper.Replace(text)
}

func (r *Report) Markdown() []byte {
	var out strings.Builder
	fmt.Fprintf(&out, "# Egress Proof\n\n**%d passed, %d failed, %d errors**\n\n", r.Summary.Passed, r.Summary.Failed, r.Summary.Errors)
	for _, item := range []struct{ name, value string }{
		{"Spec hash", r.SpecHash}, {"Namespace", r.Spec.Namespace}, {"Pod selector", r.Spec.PodSelector},
		{"Cluster context", r.Cluster.Context}, {"API server", r.Cluster.Server}, {"Cluster UID", r.Cluster.KubeSystemUID},
		{"Kubernetes version", r.Cluster.Version}, {"Probe image", r.ProbeImage},
		{"Started (UTC)", r.StartedAt.UTC().Format(time.RFC3339Nano)}, {"Finished (UTC)", r.FinishedAt.UTC().Format(time.RFC3339Nano)},
	} {
		fmt.Fprintf(&out, "- %s: %s\n", item.name, markdownText(item.value))
	}
	out.WriteString("\n## Checks\n\n| Target | Destination | Expected | Observed | Verdict | Explanation |\n| --- | --- | --- | --- | --- | --- |\n")
	for _, check := range r.Checks {
		fmt.Fprintf(&out, "| %s | %s | %s | %s | %s | %s |\n",
			markdownText(check.Target), markdownText(check.Destination), markdownText(check.Expected),
			markdownText(check.Outcome), markdownText(check.Verdict), markdownText(probe.Explain(check.Expected, check.Observation)))
	}
	if len(r.Checks) == 0 {
		out.WriteString("\nNo checks completed.\n")
	}
	out.WriteString("\n## Probe stages\n\n| Target | Destination | Stage | Address | Duration (ms) | Result |\n| --- | --- | --- | --- | --- | --- |\n")
	for _, check := range r.Checks {
		for _, stage := range check.Stages {
			result := "ok"
			if !stage.Success {
				result = "error: " + stage.Error
			}
			fmt.Fprintf(&out, "| %s | %s | %s | %s | %d | %s |\n",
				markdownText(check.Target), markdownText(check.Destination), markdownText(stage.Name),
				markdownText(stage.Address), stage.DurationMS, markdownText(result))
		}
	}
	if len(r.Errors) > 0 {
		out.WriteString("\n## Run errors\n\n")
		for _, err := range r.Errors {
			fmt.Fprintf(&out, "- %s\n", markdownText(err))
		}
	}
	out.WriteString("\nPoint-in-time connectivity evidence. Use known-live destinations and reachable controls; timeouts cannot distinguish policy drops from outages.\n\nSee evidence.json for complete observations and target identities.\n")
	return []byte(out.String())
}
