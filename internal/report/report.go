package report

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/RamazanKara/egress-proof/internal/probe"
	"github.com/RamazanKara/egress-proof/internal/spec"
)

type Cluster struct {
	Context       string `json:"context"`
	Server        string `json:"server"`
	KubeSystemUID string `json:"kubeSystemUID"`
	Version       string `json:"version"`
}

type Target struct {
	Namespace      string            `json:"namespace"`
	Pod            string            `json:"pod"`
	UID            string            `json:"uid"`
	Labels         map[string]string `json:"labels"`
	ServiceAccount string            `json:"serviceAccount"`
	Node           string            `json:"node"`
	ProbePod       string            `json:"probePod,omitempty"`
	ProbeUID       string            `json:"probeUID,omitempty"`
	ProbeImageID   string            `json:"probeImageID,omitempty"`
	CleanedUp      bool              `json:"cleanedUp"`
}

type Check struct {
	Target   string `json:"target"`
	Expected string `json:"expected"`
	Verdict  string `json:"verdict"`
	probe.Observation
}

type Summary struct {
	Passed int `json:"passed"`
	Failed int `json:"failed"`
	Errors int `json:"errors"`
}

type Report struct {
	SchemaVersion string    `json:"schemaVersion"`
	SpecHash      string    `json:"specHash"`
	Spec          spec.Spec `json:"spec"`
	Cluster       Cluster   `json:"cluster"`
	ProbeImage    string    `json:"probeImage"`
	StartedAt     time.Time `json:"startedAt"`
	FinishedAt    time.Time `json:"finishedAt"`
	Targets       []Target  `json:"targets"`
	Checks        []Check   `json:"checks"`
	Errors        []string  `json:"errors"`
	Summary       Summary   `json:"summary"`
}

func New(s spec.Spec, hash, image string) *Report {
	return &Report{
		SchemaVersion: "1", SpecHash: hash, Spec: s, ProbeImage: image, StartedAt: time.Now().UTC(),
		Targets: []Target{}, Checks: []Check{}, Errors: []string{},
	}
}

func (r *Report) Finish() {
	r.FinishedAt = time.Now().UTC()
	r.Summary = Summary{Errors: len(r.Errors)}
	for _, check := range r.Checks {
		switch check.Verdict {
		case "pass":
			r.Summary.Passed++
		case "fail":
			r.Summary.Failed++
		case "error":
			r.Summary.Errors++
		}
	}
}

func (r *Report) ExitCode() int {
	if r.Summary.Errors > 0 {
		return 2
	}
	if r.Summary.Failed > 0 {
		return 1
	}
	return 0
}

type junitSuite struct {
	XMLName    xml.Name        `xml:"testsuite"`
	Name       string          `xml:"name,attr"`
	Tests      int             `xml:"tests,attr"`
	Failures   int             `xml:"failures,attr"`
	Errors     int             `xml:"errors,attr"`
	Time       string          `xml:"time,attr"`
	Timestamp  string          `xml:"timestamp,attr"`
	Properties []junitProperty `xml:"properties>property"`
	Cases      []junitCase     `xml:"testcase"`
}

type junitProperty struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type junitCase struct {
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *junitProblem `xml:"failure,omitempty"`
	Error     *junitProblem `xml:"error,omitempty"`
	Output    string        `xml:"system-out,omitempty"`
}

type junitProblem struct {
	Message string `xml:"message,attr"`
	Text    string `xml:",chardata"`
}

func seconds(start, end time.Time) string {
	return strconv.FormatFloat(end.Sub(start).Seconds(), 'f', 3, 64)
}

func (r *Report) JUnit() ([]byte, error) {
	suite := junitSuite{
		Name: "egress-proof", Tests: len(r.Checks) + len(r.Errors), Failures: r.Summary.Failed, Errors: r.Summary.Errors,
		Time: seconds(r.StartedAt, r.FinishedAt), Timestamp: r.StartedAt.Format(time.RFC3339),
		Properties: []junitProperty{{"specHash", r.SpecHash}, {"clusterUID", r.Cluster.KubeSystemUID}, {"probeImage", r.ProbeImage}},
	}
	for _, check := range r.Checks {
		observed, err := json.Marshal(check.Observation)
		if err != nil {
			return nil, err
		}
		c := junitCase{Name: check.Destination, Classname: check.Target, Time: seconds(check.StartedAt, check.FinishedAt), Output: string(observed)}
		message := fmt.Sprintf("expected %s, observed %s", check.Expected, check.Outcome)
		if check.Verdict == "fail" {
			c.Failure = &junitProblem{Message: message, Text: string(observed)}
		} else if check.Verdict == "error" {
			c.Error = &junitProblem{Message: check.Error, Text: string(observed)}
		}
		suite.Cases = append(suite.Cases, c)
	}
	for i, err := range r.Errors {
		suite.Cases = append(suite.Cases, junitCase{
			Name: fmt.Sprintf("run-error-%d", i+1), Classname: "egress-proof", Time: "0",
			Error: &junitProblem{Message: err, Text: err},
		})
	}
	data, err := xml.MarshalIndent(suite, "", "  ")
	return append([]byte(xml.Header), data...), err
}

func (r *Report) Write(dir string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	junit, err := r.JUnit()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "evidence.json"), append(data, '\n'), 0600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "junit.xml"), append(junit, '\n'), 0600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "summary.md"), r.Markdown(), 0600)
}
