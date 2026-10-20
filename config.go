package main

import (
	"errors"
	"flag"
	"fmt"
)

// config is the gateway configuration from flags and COPILOTGW_* variables.
type config struct {
	host              string
	port              int
	tokenDir          string
	apiKey            string
	debug             bool
	responsesIDCompat bool
	autoReviewModel   string
	showVersion       bool
}

// errReported marks a command-line error, which the flag package has already
// printed together with the usage text.
var errReported = errors.New("reported")

// flagEnv names the environment variable behind each flag that has one.
var flagEnv = []struct{ flag, env string }{
	{"host", "COPILOTGW_HOST"},
	{"port", "COPILOTGW_PORT"},
	{"token-dir", "COPILOTGW_TOKEN_DIR"},
	{"api-key", "COPILOTGW_API_KEY"},
	{"debug", "COPILOTGW_DEBUG"},
	{"responses-id-compat", "COPILOTGW_RESPONSES_ID_COMPAT"},
	{"auto-review-model", "COPILOTGW_AUTO_REVIEW_MODEL"},
}

// parseConfig parses args, then fills every flag not given on the command line
// from its environment variable. A flag given explicitly always wins, including
// -debug=false; an environment value the flag would reject is an error.
func parseConfig(args []string, getenv func(string) string) (config, error) {
	var cfg config
	fs := flag.NewFlagSet("copilotgateway", flag.ContinueOnError)
	fs.StringVar(&cfg.host, "host", "127.0.0.1", "Server host (env: COPILOTGW_HOST)")
	fs.IntVar(&cfg.port, "port", 7777, "Server port (env: COPILOTGW_PORT)")
	fs.StringVar(&cfg.tokenDir, "token-dir", "", "Token storage directory (env: COPILOTGW_TOKEN_DIR, default: ~/.config/copilotgateway)")
	fs.StringVar(&cfg.apiKey, "api-key", "", "Required API key(s) for clients, comma-separated (env: COPILOTGW_API_KEY)")
	fs.BoolVar(&cfg.debug, "debug", false, "Enable debug logging (env: COPILOTGW_DEBUG)")
	fs.BoolVar(&cfg.responsesIDCompat, "responses-id-compat", true, "Normalize drifting Responses output item IDs (env: COPILOTGW_RESPONSES_ID_COMPAT)")
	fs.StringVar(&cfg.autoReviewModel, "auto-review-model", "", "Model that replaces Codex's codex-auto-review pseudo-model (env: COPILOTGW_AUTO_REVIEW_MODEL)")
	fs.BoolVar(&cfg.showVersion, "version", false, "Show version and exit")
	if err := fs.Parse(args); err != nil {
		return config{}, fmt.Errorf("%w: %w", errReported, err)
	}

	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	for _, fe := range flagEnv {
		if v := getenv(fe.env); v != "" && !explicit[fe.flag] {
			if err := fs.Set(fe.flag, v); err != nil {
				return config{}, fmt.Errorf("invalid %s: %w", fe.env, err)
			}
		}
	}
	return cfg, nil
}
