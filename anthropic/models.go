package anthropic

import (
	"context"
	"errors"
	"log/slog"
	"regexp"

	"github.com/HeyJiqingCode/CopilotGateway/copilot"
)

// modelAliases maps model name variants to their Copilot equivalents.
// Only needed for non-obvious mappings that can't be derived algorithmically.
var modelAliases = map[string]string{
	// Non-obvious version mappings (pre-4.5 naming used single-digit versions)
	"claude-opus-4": "claude-opus-4.5",
}

// versionHyphenRe matches hyphen-separated version numbers like "4-6" or "4-5"
// that appear after a letter segment (e.g. "opus-4-6"). Both digits must be single
// to avoid matching date components like "04-14" or "20-25" in "2025-04-14".
var versionHyphenRe = regexp.MustCompile(`([a-zA-Z]-)(\d)-(\d)([^0-9]|$)`)

// dateSuffixRe matches an 8-digit date suffix like "-20250514" or "-20251001"
// at the end of a model ID (optionally followed by more digits for timestamps).
var dateSuffixRe = regexp.MustCompile(`-(\d{8,})$`)

// resolveModelAlias returns the canonical model ID for Copilot's model list.
// It applies the following transformations in order:
//  1. Strip date suffixes (e.g. "-20250514")
//  2. Normalize hyphen-separated versions to dot-separated (e.g. "4-6" → "4.6")
//  3. Check explicit alias overrides for non-obvious mappings
func resolveModelAlias(modelID string) string {
	// Step 1: Strip date suffix (e.g. "claude-opus-4-6-20250514" → "claude-opus-4-6")
	stripped := dateSuffixRe.ReplaceAllString(modelID, "")
	if stripped == "" {
		stripped = modelID // safety: don't strip everything
	}

	// Step 2: Normalize hyphen-separated versions to dot-separated
	normalized := versionHyphenRe.ReplaceAllString(stripped, "${1}${2}.${3}${4}")

	// Step 3: Check explicit aliases (e.g. "claude-opus-4" → "claude-opus-4.5")
	if alias, ok := modelAliases[normalized]; ok {
		return alias
	}
	return normalized
}

// getModelInfo returns the catalog entry for an already resolved model ID, or
// nil when the model is unknown or the catalog cannot be fetched.
func (h *Handler) getModelInfo(ctx context.Context, modelID string) *copilot.ModelInfo {
	infoMap, err := h.models.GetInfo(ctx)
	if err != nil {
		// A pending sign-in is answered with its 403 right after; not worth a warning.
		if !errors.As(err, new(*copilot.ReauthRequiredError)) {
			slog.Warn("failed to fetch models for capability detection", "error", err)
		}
		return nil
	}
	return infoMap[modelID]
}
