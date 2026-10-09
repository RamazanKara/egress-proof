package probe

func Explain(expected string, obs Observation) string {
	if expected == "blocked" {
		for _, stage := range obs.Stages {
			if stage.Name == "tcp" && stage.Success {
				return "A TCP connection succeeded, so egress was allowed even if TLS or a later address failed."
			}
		}
	}
	if obs.Outcome == "error" {
		if obs.Error != "" {
			return "The probe was inconclusive: " + obs.Error
		}
		return "The probe did not establish a conclusive result."
	}
	if obs.Outcome == "unreachable" {
		if expected == "blocked" {
			return "The probe timed out, matching the blocked expectation; a timeout alone cannot prove a policy drop."
		}
		return "The probe timed out, but reachability was required."
	}
	if expected == "blocked" {
		return "The destination was reachable despite the blocked expectation."
	}
	return "The destination was reachable, matching the expectation."
}
