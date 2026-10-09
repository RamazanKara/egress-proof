package probe

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const Timeout = 5 * time.Second

type Destination struct {
	Host     string
	Port     string
	Protocol string
}

type Stage struct {
	Name       string    `json:"name"`
	Address    string    `json:"address"`
	StartedAt  time.Time `json:"startedAt"`
	DurationMS int64     `json:"durationMs"`
	Success    bool      `json:"success"`
	Error      string    `json:"error,omitempty"`
}

type Observation struct {
	Destination string    `json:"destination"`
	StartedAt   time.Time `json:"startedAt"`
	FinishedAt  time.Time `json:"finishedAt"`
	Outcome     string    `json:"outcome"`
	Error       string    `json:"error,omitempty"`
	ResolvedIPs []string  `json:"resolvedIPs,omitempty"`
	Stages      []Stage   `json:"stages"`
}

func ParseDestination(raw string) (Destination, error) {
	var d Destination
	if raw == "" || strings.TrimSpace(raw) != raw {
		return d, fmt.Errorf("destination must not be empty or padded with whitespace")
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return d, fmt.Errorf("invalid destination %q: %w", raw, err)
		}
		if u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
			return d, fmt.Errorf("destination %q must be an HTTPS URL without credentials or fragment", raw)
		}
		d = Destination{Host: u.Hostname(), Port: u.Port(), Protocol: "tls"}
		if d.Port == "" {
			if strings.HasSuffix(u.Host, ":") {
				return d, fmt.Errorf("destination %q has an empty port", raw)
			}
			d.Port = "443"
		}
	} else if strings.Contains(raw, ":") {
		host, port, err := net.SplitHostPort(raw)
		if err != nil {
			return d, fmt.Errorf("destination %q needs host:port (bracket IPv6 addresses): %w", raw, err)
		}
		d = Destination{Host: host, Port: port, Protocol: "tcp"}
	} else {
		if net.ParseIP(raw) != nil {
			return d, fmt.Errorf("IP destination %q needs a port", raw)
		}
		d = Destination{Host: raw, Protocol: "dns"}
	}
	if net.ParseIP(d.Host) == nil && !validDNSName(d.Host) {
		return d, fmt.Errorf("invalid destination host %q", d.Host)
	}
	if d.Protocol != "dns" {
		port, err := strconv.Atoi(d.Port)
		if err != nil || port < 1 || port > 65535 || strings.IndexFunc(d.Port, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return d, fmt.Errorf("destination %q needs a numeric port from 1 to 65535", raw)
		}
	}
	return d, nil
}

func validDNSName(name string) bool {
	name = strings.TrimSuffix(name, ".")
	if len(name) == 0 || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func Check(ctx context.Context, raw string) (obs Observation) {
	obs = Observation{Destination: raw, StartedAt: time.Now().UTC(), Outcome: "error", Stages: []Stage{}}
	defer func() { obs.FinishedAt = time.Now().UTC() }()
	d, err := ParseDestination(raw)
	if err != nil {
		obs.Error = err.Error()
		return
	}
	checkCtx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	ips := []string{d.Host}
	if net.ParseIP(d.Host) == nil {
		start := time.Now().UTC()
		ips, err = (&net.Resolver{PreferGo: true}).LookupHost(checkCtx, d.Host)
		obs.Stages = append(obs.Stages, stage("dns", d.Host, start, err))
		if err != nil {
			obs.Error = err.Error()
			if d.Protocol == "dns" && isTimeout(err) && ctx.Err() == nil {
				obs.Outcome = "unreachable"
			}
			return
		}
		if len(ips) == 0 {
			obs.Error = "DNS returned no addresses"
			return
		}
	}
	if err := ctx.Err(); err != nil {
		obs.Error = err.Error()
		return
	}
	slices.Sort(ips)
	ips = slices.Compact(ips)
	obs.ResolvedIPs = ips
	if d.Protocol == "dns" {
		obs.Outcome = "reachable"
		return
	}
	allTimedOut := true
	var observedErrors []string
	for i, ip := range ips {
		if checkCtx.Err() != nil {
			allTimedOut = false
			observedErrors = append(observedErrors, "check deadline reached before testing all addresses")
			break
		}
		// Give every resolved address a chance; one allowed address is an egress leak.
		deadline, _ := checkCtx.Deadline()
		addressCtx, addressCancel := context.WithTimeout(checkCtx, time.Until(deadline)/time.Duration(len(ips)-i))
		address := net.JoinHostPort(ip, d.Port)
		start := time.Now().UTC()
		conn, dialErr := (&net.Dialer{}).DialContext(addressCtx, "tcp", address)
		obs.Stages = append(obs.Stages, stage("tcp", address, start, dialErr))
		if dialErr != nil {
			allTimedOut = allTimedOut && isTimeout(dialErr)
			observedErrors = append(observedErrors, dialErr.Error())
		} else {
			allTimedOut = false
			if d.Protocol == "tls" {
				start = time.Now().UTC()
				tlsConn := tls.Client(conn, &tls.Config{ServerName: d.Host, MinVersion: tls.VersionTLS12})
				tlsErr := tlsConn.HandshakeContext(addressCtx)
				obs.Stages = append(obs.Stages, stage("tls", address, start, tlsErr))
				if tlsErr != nil {
					observedErrors = append(observedErrors, tlsErr.Error())
				} else {
					obs.Outcome = "reachable"
				}
			} else {
				obs.Outcome = "reachable"
			}
			conn.Close()
		}
		addressCancel()
	}
	if ctx.Err() != nil {
		obs.Outcome = "error"
		observedErrors = append(observedErrors, ctx.Err().Error())
	} else if allTimedOut {
		obs.Outcome = "unreachable"
	}
	obs.Error = strings.Join(observedErrors, "; ")
	return
}

func stage(name, address string, started time.Time, err error) Stage {
	s := Stage{Name: name, Address: address, StartedAt: started, DurationMS: time.Since(started).Milliseconds(), Success: err == nil}
	if err != nil {
		s.Error = err.Error()
	}
	return s
}

func isTimeout(err error) bool {
	var e net.Error
	return errors.As(err, &e) && e.Timeout()
}

func Verdict(expected string, obs Observation) string {
	if expected == "blocked" {
		for _, s := range obs.Stages {
			if s.Name == "tcp" && s.Success {
				return "fail"
			}
		}
	}
	if obs.Outcome == "error" {
		return "error"
	}
	if expected == "reachable" && obs.Outcome == "reachable" || expected == "blocked" && obs.Outcome == "unreachable" {
		return "pass"
	}
	return "fail"
}
