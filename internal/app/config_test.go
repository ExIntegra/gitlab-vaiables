package app

import "testing"

func cleanConfig(t *testing.T) {
	t.Helper()
	for _, key := range []string{"APP_ADDR", "APP_ORIGIN", "GITLAB_URL", "APP_DEMO", "SESSION_TTL", "TRACKING_FILE"} {
		t.Setenv(key, "")
	}
}

func TestConfigDefaultsAndHTTPS(t *testing.T) {
	cleanConfig(t)
	cfg, err := LoadConfig()
	if err != nil || cfg.Addr != "127.0.0.1:8080" || cfg.Origin != "http://localhost:8080" || cfg.GitLabURL != "https://gitlab.com" || cfg.Demo {
		t.Fatalf("defaults %+v %v", cfg, err)
	}
	t.Setenv("APP_ORIGIN", "https://variables.example.com/")
	cfg, err = LoadConfig()
	if err != nil || cfg.Origin != "https://variables.example.com" {
		t.Fatalf("HTTPS: %+v %v", cfg, err)
	}
}

func TestInvalidConfigIsRejected(t *testing.T) {
	for _, input := range []struct{ key, value string }{
		{"APP_ORIGIN", "http://public.example.com"}, {"APP_ORIGIN", "https://user:password@example.com"},
		{"APP_ORIGIN", "https://example.com/path"}, {"APP_ORIGIN", "https://example.com?token=x"},
		{"APP_ADDR", "8080"}, {"APP_DEMO", "yes"}, {"SESSION_TTL", "1s"}, {"SESSION_TTL", "48h"},
	} {
		t.Run(input.key+input.value, func(t *testing.T) {
			cleanConfig(t)
			t.Setenv(input.key, input.value)
			if _, err := LoadConfig(); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}
