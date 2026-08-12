package platformevents

import (
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
)

var (
	platformBaseURLPattern    = regexp.MustCompile(`^https?://(?:\[[0-9A-Fa-f:.%]+\]|[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?)(?::[0-9]{1,5})?(?:/[A-Za-z0-9._~!$&'()*+,;=:@%/-]*)?$`)
	platformRequestURLPattern = regexp.MustCompile(`^(?:https?|wss?)://(?:\[[0-9A-Fa-f:.%]+\]|[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?)(?::[0-9]{1,5})?(?:/[A-Za-z0-9._~!$&'()*+,;=:@%/-]*)?(?:\?[A-Za-z0-9._~!$&'()*+,;=:@%/?-]*)?$`)
	versionPattern            = regexp.MustCompile(`^[A-Za-z0-9._-]{0,128}$`)
	eventNamePattern          = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	hostLabelPattern          = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?$`)
)

type trustedOrigin struct {
	scheme   string
	hostname string
	port     string
}

func validatePlatformBaseURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if !platformBaseURLPattern.MatchString(raw) {
		return nil, fmt.Errorf("platform URL has an unsupported format")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse platform URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("platform URL must use http or https")
	}
	if u.Opaque != "" || u.User != nil || u.Host == "" || u.Hostname() == "" {
		return nil, fmt.Errorf("platform URL must contain only an origin and path")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, fmt.Errorf("platform URL must not contain a query or fragment")
	}
	if u.RawPath != "" || strings.ContainsAny(u.Path, "\x00\r\n\t\\") {
		return nil, fmt.Errorf("platform URL contains an unsafe path")
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return nil, fmt.Errorf("platform URL path must not contain dot segments")
		}
	}
	canonicalPath := strings.TrimRight(u.Path, "/")
	if canonicalPath == "" && u.Path == "/" {
		canonicalPath = "/"
	}
	if u.Path != "" && path.Clean(u.Path) != canonicalPath {
		return nil, fmt.Errorf("platform URL path is not canonical")
	}
	if err := validateHostname(u.Hostname()); err != nil {
		return nil, err
	}
	if port := u.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return nil, fmt.Errorf("platform URL has an invalid port")
		}
	}

	u.Host = strings.ToLower(u.Host)
	return u, nil
}

func validateHostname(hostname string) error {
	hostname = strings.ToLower(hostname)
	address := hostname
	if zone := strings.LastIndex(address, "%"); zone >= 0 {
		address = address[:zone]
		if net.ParseIP(address) == nil || !strings.Contains(address, ":") {
			return fmt.Errorf("platform URL has an invalid scoped address")
		}
	}
	if net.ParseIP(address) != nil {
		return nil
	}
	if len(hostname) > 253 {
		return fmt.Errorf("platform URL host is too long")
	}
	for _, label := range strings.Split(hostname, ".") {
		if label == "" || len(label) > 63 || !hostLabelPattern.MatchString(label) {
			return fmt.Errorf("platform URL has an invalid host")
		}
	}
	return nil
}

func originFor(u *url.URL) trustedOrigin {
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "ws":
		scheme = "http"
	case "wss":
		scheme = "https"
	}
	port := u.Port()
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else if scheme == "http" {
			port = "80"
		}
	}
	return trustedOrigin{scheme: scheme, hostname: strings.ToLower(u.Hostname()), port: port}
}

func CanonicalOrigin(raw string) (string, error) {
	u, err := validatePlatformBaseURL(raw)
	if err != nil {
		return "", err
	}
	origin := originFor(u)
	return origin.scheme + "://" + net.JoinHostPort(origin.hostname, origin.port), nil
}

func isValidRedirect(candidate *url.URL, trusted trustedOrigin) bool {
	if candidate == nil || candidate.Opaque != "" || candidate.User != nil || candidate.Hostname() == "" {
		return false
	}
	actual := originFor(candidate)
	return actual == trusted
}
