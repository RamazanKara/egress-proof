package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/RamazanKara/egress-proof/internal/report"
	"github.com/RamazanKara/egress-proof/internal/runner"
	"github.com/RamazanKara/egress-proof/internal/spec"
	"k8s.io/client-go/discovery"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("egress-proof", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("spec", "", "YAML spec to test (required)")
	image := fs.String("image", "egress-proof-probe:dev", "probe image available to the cluster")
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig path (defaults to KUBECONFIG or ~/.kube/config)")
	out := fs.String("out", "evidence", "directory for evidence.json and junit.xml")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *path == "" || *image == "" || *out == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: egress-proof --spec FILE [--image IMAGE] [--kubeconfig FILE] [--out DIR]")
		return 2
	}
	data, err := os.ReadFile(*path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	s, hash, err := spec.Parse(data)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	r := report.New(s, hash, *image)
	if err := runCluster(ctx, *kubeconfig, r); err != nil {
		r.Errors = append(r.Errors, err.Error())
	}
	r.Finish()
	if err := r.Write(*out); err != nil {
		fmt.Fprintf(stderr, "write evidence: %v\n", err)
		return 2
	}
	for _, check := range r.Checks {
		fmt.Fprintf(stdout, "%s %s %s (expected %s, observed %s)\n", check.Verdict, check.Target, check.Destination, check.Expected, check.Outcome)
		if check.Verdict == "error" {
			fmt.Fprintln(stderr, check.Error)
		}
	}
	for _, err := range r.Errors {
		fmt.Fprintln(stderr, err)
	}
	fmt.Fprintf(stdout, "%d passed, %d failed, %d errors; reports: %s\n", r.Summary.Passed, r.Summary.Failed, r.Summary.Errors, *out)
	return r.ExitCode()
}

func runCluster(ctx context.Context, path string, r *report.Report) error {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.ExplicitPath = path
	loader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{})
	raw, err := loader.RawConfig()
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}
	r.Cluster.Context = raw.CurrentContext
	if r.Spec.Namespace == "" {
		r.Spec.Namespace, _, err = loader.Namespace()
		if err != nil {
			return fmt.Errorf("load namespace: %w", err)
		}
	}
	cfg, err := loader.ClientConfig()
	if err != nil {
		return fmt.Errorf("load Kubernetes client: %w", err)
	}
	server, err := url.Parse(cfg.Host)
	if err != nil {
		return err
	}
	server.User, server.RawQuery, server.Fragment = nil, "", ""
	r.Cluster.Server = server.String()
	cfg.Timeout = 15 * time.Second
	cfg.UserAgent = "egress-proof/0.1"
	client, err := typedcorev1.NewForConfig(cfg)
	if err != nil {
		return err
	}
	discover, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return err
	}
	version, err := discover.ServerVersion()
	if err != nil {
		return fmt.Errorf("cluster version: %w", err)
	}
	r.Cluster.Version = version.GitVersion
	runner.Run(ctx, client, r)
	return nil
}
