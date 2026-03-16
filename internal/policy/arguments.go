package policy

import (
	"encoding/json"
	"regexp"
	"slices"
	"sync"
	"unicode/utf8"

	"github.com/rebuno/rebuno/internal/domain"
)

var regexpCache sync.Map // pattern string -> *regexp.Regexp

func matchArguments(predicates []domain.ArgumentPredicate, raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}

	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return false
	}

	for _, pred := range predicates {
		if !evaluatePredicate(pred, args) {
			return false
		}
	}
	return true
}

func evaluatePredicate(pred domain.ArgumentPredicate, args map[string]any) bool {
	val, exists := args[pred.Field]

	if pred.Required {
		if !exists {
			return false
		}
		if s, ok := val.(string); ok && s == "" {
			return false
		}
	}

	if !exists {
		return true
	}

	if pred.Pattern != "" && !checkPattern(pred.Pattern, val) {
		return false
	}
	if len(pred.OneOf) > 0 && !checkOneOf(pred.OneOf, val) {
		return false
	}
	if pred.Min != nil && !checkMin(*pred.Min, val) {
		return false
	}
	if pred.Max != nil && !checkMax(*pred.Max, val) {
		return false
	}
	if pred.MaxLength != nil && !checkMaxLength(*pred.MaxLength, val) {
		return false
	}

	return true
}

func checkPattern(pattern string, val any) bool {
	s, ok := val.(string)
	if !ok {
		return false
	}
	re, err := compileRegexp(pattern)
	if err != nil {
		return false
	}
	return re.MatchString(s)
}

func compileRegexp(pattern string) (*regexp.Regexp, error) {
	if cached, ok := regexpCache.Load(pattern); ok {
		return cached.(*regexp.Regexp), nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	regexpCache.Store(pattern, re)
	return re, nil
}

func checkOneOf(allowed []string, val any) bool {
	s, ok := val.(string)
	if !ok {
		return false
	}
	return slices.Contains(allowed, s)
}

func checkMin(min float64, val any) bool {
	n, ok := toFloat64(val)
	if !ok {
		return false
	}
	return n >= min
}

func checkMax(max float64, val any) bool {
	n, ok := toFloat64(val)
	if !ok {
		return false
	}
	return n <= max
}

func checkMaxLength(maxLen int, val any) bool {
	s, ok := val.(string)
	if !ok {
		return false
	}
	return utf8.RuneCountInString(s) <= maxLen
}

func toFloat64(val any) (float64, bool) {
	switch v := val.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	default:
		return 0, false
	}
}
