package haproxy

import (
	"fmt"
	"sort"
)

// Match specificity ranks, most specific first
const (
	rankExact = iota
	rankPrefix
	rankRegex
)

// domainTypeRank orders match types from most to least specific.
// An unset type is an exact match, the default of parseDomainMapping.
func domainTypeRank(domainType DomainType) int {
	switch domainType {
	case DomainTypeExact:
		return rankExact
	case DomainTypePrefix:
		return rankPrefix
	case DomainTypeRegex:
		return rankRegex
	default:
		return rankExact
	}
}

// sortFrontendRulesBySpecificity returns the rules ordered exact → prefix → regex.
// HAProxy takes the first matching use_backend rule, so a catch-all regex written
// before an exact host rule makes that host unreachable. Rules of the same type keep
// their relative order, so overlapping regexes still resolve by registration order.
func sortFrontendRulesBySpecificity(rules []FrontendRule) []FrontendRule {
	sorted := append([]FrontendRule(nil), rules...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return domainTypeRank(sorted[i].Type) < domainTypeRank(sorted[j].Type)
	})
	return sorted
}

func frontendRulesAreSorted(rules []FrontendRule) bool {
	return sort.SliceIsSorted(rules, func(i, j int) bool {
		return domainTypeRank(rules[i].Type) < domainTypeRank(rules[j].Type)
	})
}

// ReorderFrontendRules rewrites the frontend rules in specificity order. It repairs
// an order that was built up before the connector sorted on write. When the order is
// already correct it writes nothing, so it costs no config version bump and no reload.
func (c *Client) ReorderFrontendRules(frontend string) error {
	currentRules, err := c.GetFrontendRules(frontend)
	if err != nil {
		return fmt.Errorf("failed to get current rules: %w", err)
	}

	if frontendRulesAreSorted(currentRules) {
		return nil
	}

	transactionID, err := c.createTransaction()
	if err != nil {
		return fmt.Errorf("failed to create transaction: %w", err)
	}

	if err := c.setFrontendRulesInTransaction(frontend, currentRules, transactionID); err != nil {
		return fmt.Errorf("failed to reorder rules: %w", err)
	}

	if err := c.commitTransaction(transactionID); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}
