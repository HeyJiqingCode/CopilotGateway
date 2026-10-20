package main

import (
	"strings"
	"testing"
)

func env(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

func TestParseConfig_Defaults(t *testing.T) {
	cfg, err := parseConfig(nil, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := config{host: "127.0.0.1", port: 7777, responsesIDCompat: true}
	if cfg != want {
		t.Fatalf("config = %+v, want %+v", cfg, want)
	}
}

func TestParseConfig_EnvironmentFillsUnsetFlags(t *testing.T) {
	cfg, err := parseConfig(nil, env(map[string]string{
		"COPILOTGW_HOST":                "0.0.0.0",
		"COPILOTGW_PORT":                "8080",
		"COPILOTGW_TOKEN_DIR":           "/data",
		"COPILOTGW_API_KEY":             "k1,k2",
		"COPILOTGW_DEBUG":               "1",
		"COPILOTGW_RESPONSES_ID_COMPAT": "false",
		"COPILOTGW_AUTO_REVIEW_MODEL":   "gpt-6-luna",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := config{host: "0.0.0.0", port: 8080, tokenDir: "/data", apiKey: "k1,k2", debug: true, autoReviewModel: "gpt-6-luna"}
	if cfg != want {
		t.Fatalf("config = %+v, want %+v", cfg, want)
	}
}

func TestParseConfig_ExplicitFlagsBeatEnvironment(t *testing.T) {
	cfg, err := parseConfig(
		[]string{"-debug=false", "-responses-id-compat=true", "-port", "9000", "-auto-review-model", "gpt-5.6-luna"},
		env(map[string]string{
			"COPILOTGW_DEBUG":               "true",
			"COPILOTGW_RESPONSES_ID_COMPAT": "false",
			"COPILOTGW_PORT":                "8080",
			"COPILOTGW_AUTO_REVIEW_MODEL":   "gpt-6-luna",
		}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.debug || !cfg.responsesIDCompat || cfg.port != 9000 || cfg.autoReviewModel != "gpt-5.6-luna" {
		t.Fatalf("config = %+v, want the command-line values", cfg)
	}
}

func TestParseConfig_InvalidEnvironmentIsAnError(t *testing.T) {
	for name, value := range map[string]string{
		"COPILOTGW_PORT":                "seventy",
		"COPILOTGW_DEBUG":               "maybe",
		"COPILOTGW_RESPONSES_ID_COMPAT": "sometimes",
	} {
		_, err := parseConfig(nil, env(map[string]string{name: value}))
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s=%s: error = %v, want one naming the variable", name, value, err)
		}
	}
}
