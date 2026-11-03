package app

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	Addr         string
	Origin       string
	GitLabURL    string
	Demo         bool
	SessionTTL   time.Duration
	TrackingFile string
}

func LoadConfig() (Config, error) {
	c := Config{Addr: env("APP_ADDR", "127.0.0.1:8080"), Origin: env("APP_ORIGIN", "http://localhost:8080"), GitLabURL: env("GITLAB_URL", "https://gitlab.com"), SessionTTL: time.Hour, TrackingFile: env("TRACKING_FILE", "data/tracking.json")}
	switch env("APP_DEMO", "false") {
	case "true":
		c.Demo = true
	case "false":
	default:
		return c, fmt.Errorf("APP_DEMO must be true or false")
	}
	if v := os.Getenv("SESSION_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 5*time.Minute || d > 24*time.Hour {
			return c, fmt.Errorf("SESSION_TTL must be between 5m and 24h")
		}
		c.SessionTTL = d
	}
	u, err := url.Parse(c.Origin)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && !(u.Scheme == "http" && loopback(u.Hostname()))) {
		return c, fmt.Errorf("APP_ORIGIN must be an HTTPS origin (HTTP allowed on localhost)")
	}
	c.Origin = u.Scheme + "://" + u.Host
	if _, _, err := net.SplitHostPort(c.Addr); err != nil {
		return c, fmt.Errorf("APP_ADDR must be host:port")
	}
	return c, nil
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
