package datasheet

import (
	"regexp"
	"slices"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// The datasheet prices models by the vendor that bills them, so it has no rows for
// subscription gateways that resell those models under their own ids. Requests through
// these providers are priced at the list price of the upstream model instead.
var subscriptionPricingProviders = map[string]struct{}{
	string(schemas.Kiro):        {},
	string(schemas.Antigravity): {},
}

// subscriptionPreferredProviders ranks the vendors whose rows win when several providers
// carry the same base model. Providers not listed rank after these.
var subscriptionPreferredProviders = []string{
	"anthropic", "openai", "gemini", "deepseek", "minimax", "zai", "dashscope", "moonshot", "mistral", "xai", "vertex",
}

// subscriptionModelAliases maps wire ids that are not catalog names to the model they serve.
var subscriptionModelAliases = map[string]string{
	"gemini-pro-agent": "gemini-3.1-pro",
}

var (
	// subscriptionSuffixPatterns are the variant markers a gateway appends to an upstream
	// model id: dated snapshots, thinking/tiered builds, reasoning-effort tiers and ".0" versions.
	subscriptionSuffixPatterns = []*regexp.Regexp{
		regexp.MustCompile(`-\d{8}$`),
		regexp.MustCompile(`-\d{4}-\d{2}-\d{2}$`),
		regexp.MustCompile(`-(thinking|tiered)$`),
		regexp.MustCompile(`-(extra-low|minimal|low|medium|mid|high|xhigh|max)$`),
		regexp.MustCompile(`\.0$`),
	}
	dottedVersionPattern  = regexp.MustCompile(`(\d)\.(\d)`)
	deepseekVersionPrefix = regexp.MustCompile(`^deepseek-(\d)`)
)

func isSubscriptionPricingProvider(provider string) bool {
	_, ok := subscriptionPricingProviders[provider]
	return ok
}

// pricingBaseKey is the pricingKeysByBase key: lowercase base model | mode.
func pricingBaseKey(baseModel, mode string) string {
	return strings.ToLower(baseModel) + "|" + mode
}

// hasUsablePricing reports whether a row carries a rate that can price a request. Rows with
// no token rate on a token mode (the datasheet's own subscription entries, such as chatgpt/*)
// would price every request at zero, and negative rates are sentinels (openrouter/auto), so
// neither is eligible as an upstream price.
func hasUsablePricing(row configstoreTables.TableModelPricing) bool {
	if (row.InputCostPerToken != nil && *row.InputCostPerToken < 0) || (row.OutputCostPerToken != nil && *row.OutputCostPerToken < 0) {
		return false
	}
	switch row.Mode {
	case "chat", "responses", "completion":
		return row.InputCostPerToken != nil || row.OutputCostPerToken != nil
	}
	return true
}

// subscriptionModelCandidates lists the catalog names a gateway model id may stand for, as
// groups ordered most specific first: the id itself, then each form with variant suffixes
// stripped. A group holds the spellings of one form (dotted versions also dashed, as in
// "claude-opus-4.8" -> "claude-opus-4-8", the Anthropic spelling), which compete equally.
// The "auto" router picks its model per request, so it has no list price.
func subscriptionModelCandidates(model string) [][]string {
	m := strings.ToLower(strings.TrimSpace(model))
	m = strings.TrimPrefix(m, string(schemas.Kiro)+"/")
	m = strings.TrimPrefix(m, string(schemas.Antigravity)+"/")
	if rest := strings.TrimPrefix(m, "kiro-"); rest != "" {
		m = rest
	}
	if m == "" || m == "auto" {
		return nil
	}

	stages := []string{m}
	for changed := true; changed; {
		changed = false
		last := stages[len(stages)-1]
		for _, pattern := range subscriptionSuffixPatterns {
			if stripped := pattern.ReplaceAllString(last, ""); stripped != last && stripped != "" {
				stages = append(stages, stripped)
				changed = true
				break
			}
		}
	}

	var groups [][]string
	seen := make(map[string]struct{}, len(stages)*3)
	for _, stage := range stages {
		var group []string
		add := func(c string) {
			if _, dup := seen[c]; dup {
				return
			}
			seen[c] = struct{}{}
			group = append(group, c)
		}
		add(stage)
		if alias, ok := subscriptionModelAliases[stage]; ok {
			add(alias)
		}
		if dashed := dottedVersionPattern.ReplaceAllString(stage, "$1-$2"); dashed != stage {
			add(dashed)
		}
		if versioned := deepseekVersionPrefix.ReplaceAllString(stage, "deepseek-v$1"); versioned != stage {
			add(versioned)
		}
		if len(group) > 0 {
			groups = append(groups, group)
		}
	}
	return groups
}

// subscriptionBasePricing resolves the upstream list price for a subscription-gateway model.
// Each candidate name is matched to datasheet rows by canonical base model; among the rows
// found, the preferred vendor wins, then the exact request mode over its chat/responses
// counterpart, then a row named exactly like the candidate. Caller MUST hold s.mu.
func (s *Store) subscriptionBasePricing(model, mode, fallbackMode string, hasFallbackMode bool) (*configstoreTables.TableModelPricing, bool) {
	modes := []string{mode}
	if hasFallbackMode {
		modes = append(modes, fallbackMode)
	}

	for _, group := range subscriptionModelCandidates(model) {
		var best *subscriptionMatch
		for _, name := range group {
			bases := make([]string, 0, 2)
			if base, ok := s.baseModelIndex[name]; ok {
				bases = append(bases, base)
			}
			bases = append(bases, name)

			for _, base := range bases {
				for modeIx, m := range modes {
					for _, key := range s.pricingKeysByBase[pricingBaseKey(base, m)] {
						row, ok := s.pricingData[key]
						if !ok {
							continue
						}
						match := &subscriptionMatch{
							row:     row,
							key:     key,
							rank:    subscriptionProviderRank(row.Provider),
							modeIx:  modeIx,
							inexact: !slices.Contains(group, row.Model),
						}
						if best == nil || match.beats(best) {
							best = match
						}
					}
				}
			}
		}
		if best != nil {
			s.logger.Debug("priced subscription model %s as %s/%s", model, best.row.Provider, best.row.Model)
			return &best.row, true
		}
	}
	return nil, false
}

func subscriptionProviderRank(provider string) int {
	for i, preferred := range subscriptionPreferredProviders {
		if provider == preferred {
			return i
		}
	}
	return len(subscriptionPreferredProviders)
}

// subscriptionMatch is an upstream row considered for a subscription model's price.
type subscriptionMatch struct {
	row     configstoreTables.TableModelPricing
	key     string
	rank    int
	modeIx  int
	inexact bool
}

// beats orders matches by (vendor rank, mode, exact name, key). The key is the final
// tiebreaker so the choice never depends on map iteration order.
func (m *subscriptionMatch) beats(o *subscriptionMatch) bool {
	if m.rank != o.rank {
		return m.rank < o.rank
	}
	if m.modeIx != o.modeIx {
		return m.modeIx < o.modeIx
	}
	if m.inexact != o.inexact {
		return !m.inexact
	}
	return m.key < o.key
}
