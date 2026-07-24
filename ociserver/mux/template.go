package mux

import (
	"fmt"
	"maps"
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"
)

const (
	defaultPlaceholderExpression = `[^/]+`
	defaultWildcardExpression    = `.+`
)

type compiledTemplate struct {
	regex          *regexp.Regexp
	varNames       []string
	remainderIndex int
}

// ValidPattern reports whether pattern is a valid route template. Route
// templates begin with '/', contain literal path segments, and may use
// :placeholder or *wildcard segments.
func ValidPattern(pattern string) error {
	_, err := compileTemplate(pattern, nil, false)
	return err
}

func compileTemplate(pattern string, constraints map[string]string, prefix bool) (compiledTemplate, error) {
	if pattern == "" || pattern[0] != '/' {
		return compiledTemplate{}, fmt.Errorf("route template must begin with '/'")
	}
	if prefix && pattern != "/" && strings.HasSuffix(pattern, "/") {
		return compiledTemplate{}, fmt.Errorf("route prefix must not end with '/'")
	}
	if prefix && pattern == "/" {
		re := regexp.MustCompile(`^(/.*)$`)
		return compiledTemplate{
			regex:          re,
			varNames:       captureNames(re),
			remainderIndex: 1,
		}, nil
	}

	var expression strings.Builder
	expression.WriteByte('^')

	seen := make(map[string]struct{})
	segments := strings.Split(pattern, "/")
	for i, segment := range segments {
		if i > 0 {
			expression.WriteByte('/')
		}
		if segment == "" {
			continue
		}

		marker := segment[0]
		if marker != ':' && marker != '*' {
			expression.WriteString(regexp.QuoteMeta(segment))
			continue
		}

		name := segment[1:]
		if !validIdentifier(name) {
			return compiledTemplate{}, fmt.Errorf("invalid parameter %q", segment)
		}
		if _, ok := seen[name]; ok {
			return compiledTemplate{}, fmt.Errorf("parameter %q appears more than once", name)
		}
		seen[name] = struct{}{}

		if prefix && marker == '*' {
			return compiledTemplate{}, fmt.Errorf("wildcard parameter %q is not allowed in a route prefix", name)
		}

		matcher := defaultPlaceholderExpression
		if marker == '*' {
			matcher = defaultWildcardExpression
		}
		if constraint, ok := constraints[name]; ok {
			matcher = constraint
		}
		expression.WriteString("(?P<")
		expression.WriteString(name)
		expression.WriteString(">(?:")
		expression.WriteString(matcher)
		expression.WriteString("))")
	}

	remainderIndex := 0
	if prefix {
		// A mounted prefix owns both its exact path and everything below it.
		// The optional capture preserves the leading slash for the child router.
		expression.WriteString(`(?:(/.*))?$`)
	} else {
		expression.WriteByte('$')
	}

	re, err := regexp.Compile(expression.String())
	if err != nil {
		return compiledTemplate{}, err
	}
	if prefix {
		remainderIndex = re.NumSubexp()
	}
	return compiledTemplate{
		regex:          re,
		varNames:       captureNames(re),
		remainderIndex: remainderIndex,
	}, nil
}

func validIdentifier(name string) bool {
	for i, r := range name {
		if i == 0 {
			if !isASCIILetter(r) {
				return false
			}
			continue
		}
		if !isASCIILetter(r) && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return name != ""
}

func isASCIILetter(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}

func validateConstraint(name, expression string) error {
	if !validIdentifier(name) {
		return fmt.Errorf("invalid constraint name %q", name)
	}
	if expression == "" {
		return fmt.Errorf("constraint %q has an empty expression", name)
	}

	re, err := regexp.Compile(expression)
	if err != nil {
		return fmt.Errorf("constraint %q: %w", name, err)
	}
	if re.NumSubexp() != 0 {
		return fmt.Errorf("constraint %q must not contain capturing groups", name)
	}
	if re.MatchString("") {
		return fmt.Errorf("constraint %q must not match an empty string", name)
	}

	parsed, err := syntax.Parse(expression, syntax.Perl)
	if err != nil {
		return fmt.Errorf("constraint %q: %w", name, err)
	}
	if containsAnchor(parsed) {
		return fmt.Errorf("constraint %q must not contain anchors", name)
	}
	return nil
}

func containsAnchor(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText:
		return true
	}
	return slices.ContainsFunc(re.Sub, containsAnchor)
}

func cloneConstraints(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	maps.Copy(dst, src)
	return dst
}
