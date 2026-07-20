// Package assert DSL — a minimal expression evaluator for custom assertions.
//
// Grammar (informal):
//
//	expr   = or
//	or     = and ( "or" and )*
//	and    = not ( "and" not )*
//	not    = "not" primary | primary
//	primary = "(" expr ")" | comparison
//	comparison = term ( cmpOp term )?
//	term   = int_literal | func_call
//	func_call = IDENT "(" arglist? ")"
//	arglist   = arg ( "," arg )*
//	arg       = "where" field op value | IDENT | int_literal
//	cmpOp  = ">=" | "<=" | ">" | "<" | "==" | "!="
//
// Supported functions:
//
//   - count(kind)                       → int: count of events with that kind
//   - count(kind where field==value)    → int: filtered count
//
// Supported fields in where-clauses: tool, method, action, key, source.
//
// See internal/assert/dsl_grammar.md for the canonical grammar reference.
package assert

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/seanrobmerriam/agentchaos/internal/event"
)

// Evaluate parses and evaluates a DSL expression against log. It returns
// (true, nil) when the expression holds, (false, nil) when it does not, and
// (false, err) when the expression cannot be parsed or evaluated.
func Evaluate(expr string, log *event.Log) (bool, error) {
	p := &dslParser{src: expr, pos: 0}
	v, err := p.parseOr(log)
	if err != nil {
		return false, fmt.Errorf("DSL %q: %w", expr, err)
	}
	b, err := toBool(v)
	if err != nil {
		return false, fmt.Errorf("DSL %q: %w", expr, err)
	}
	p.skipWS()
	if p.pos < len(p.src) {
		return false, fmt.Errorf("DSL %q: unexpected trailing input %q", expr, p.src[p.pos:])
	}
	return b, nil
}

// ---------------------------------------------------------------------------
// Value types
// ---------------------------------------------------------------------------

type dslValue struct {
	i   int
	b   bool
	isB bool // if true, the value is boolean; otherwise integer
}

func intVal(i int) dslValue  { return dslValue{i: i} }
func boolVal(b bool) dslValue { return dslValue{b: b, isB: true} }

func toBool(v dslValue) (bool, error) {
	if v.isB {
		return v.b, nil
	}
	return false, fmt.Errorf("expected boolean, got integer %d", v.i)
}

func toInt(v dslValue) (int, error) {
	if !v.isB {
		return v.i, nil
	}
	return 0, fmt.Errorf("expected integer, got boolean")
}

// ---------------------------------------------------------------------------
// Parser
// ---------------------------------------------------------------------------

type dslParser struct {
	src string
	pos int
}

func (p *dslParser) skipWS() {
	for p.pos < len(p.src) && unicode.IsSpace(rune(p.src[p.pos])) {
		p.pos++
	}
}

func (p *dslParser) peek() string { return p.src[p.pos:] }

func (p *dslParser) tryKeyword(kw string) bool {
	p.skipWS()
	rest := p.src[p.pos:]
	if !strings.HasPrefix(strings.ToLower(rest), kw) {
		return false
	}
	after := len(kw)
	if after < len(rest) && (unicode.IsLetter(rune(rest[after])) || unicode.IsDigit(rune(rest[after])) || rest[after] == '_') {
		return false // not a word boundary
	}
	p.pos += len(kw)
	return true
}

func (p *dslParser) tryOp(op string) bool {
	p.skipWS()
	if strings.HasPrefix(p.src[p.pos:], op) {
		p.pos += len(op)
		return true
	}
	return false
}

// parseOr handles "and (or and)*"
func (p *dslParser) parseOr(log *event.Log) (dslValue, error) {
	left, err := p.parseAnd(log)
	if err != nil {
		return dslValue{}, err
	}
	for {
		if !p.tryKeyword("or") {
			break
		}
		right, err := p.parseAnd(log)
		if err != nil {
			return dslValue{}, err
		}
		lb, err := toBool(left)
		if err != nil {
			return dslValue{}, err
		}
		rb, err := toBool(right)
		if err != nil {
			return dslValue{}, err
		}
		left = boolVal(lb || rb)
	}
	return left, nil
}

// parseAnd handles "not (and not)*"
func (p *dslParser) parseAnd(log *event.Log) (dslValue, error) {
	left, err := p.parseNot(log)
	if err != nil {
		return dslValue{}, err
	}
	for {
		if !p.tryKeyword("and") {
			break
		}
		right, err := p.parseNot(log)
		if err != nil {
			return dslValue{}, err
		}
		lb, err := toBool(left)
		if err != nil {
			return dslValue{}, err
		}
		rb, err := toBool(right)
		if err != nil {
			return dslValue{}, err
		}
		left = boolVal(lb && rb)
	}
	return left, nil
}

// parseNot handles "not primary | primary"
func (p *dslParser) parseNot(log *event.Log) (dslValue, error) {
	if p.tryKeyword("not") {
		v, err := p.parsePrimary(log)
		if err != nil {
			return dslValue{}, err
		}
		b, err := toBool(v)
		if err != nil {
			return dslValue{}, err
		}
		return boolVal(!b), nil
	}
	return p.parsePrimary(log)
}

// parsePrimary handles parenthesized expressions and comparisons.
func (p *dslParser) parsePrimary(log *event.Log) (dslValue, error) {
	p.skipWS()
	if p.pos < len(p.src) && p.src[p.pos] == '(' {
		p.pos++
		v, err := p.parseOr(log)
		if err != nil {
			return dslValue{}, err
		}
		p.skipWS()
		if p.pos >= len(p.src) || p.src[p.pos] != ')' {
			return dslValue{}, fmt.Errorf("expected ')'")
		}
		p.pos++
		return v, nil
	}
	return p.parseComparison(log)
}

// parseComparison handles "term (cmpOp term)?"
func (p *dslParser) parseComparison(log *event.Log) (dslValue, error) {
	left, err := p.parseTerm(log)
	if err != nil {
		return dslValue{}, err
	}

	p.skipWS()
	var op string
	for _, candidate := range []string{">=", "<=", "!=", ">", "<", "=="} {
		if p.tryOp(candidate) {
			op = candidate
			break
		}
	}
	if op == "" {
		return left, nil // no comparison operator — return the term as-is
	}

	right, err := p.parseTerm(log)
	if err != nil {
		return dslValue{}, err
	}

	li, err := toInt(left)
	if err != nil {
		return dslValue{}, fmt.Errorf("left side of %s: %w", op, err)
	}
	ri, err := toInt(right)
	if err != nil {
		return dslValue{}, fmt.Errorf("right side of %s: %w", op, err)
	}

	switch op {
	case ">=":
		return boolVal(li >= ri), nil
	case "<=":
		return boolVal(li <= ri), nil
	case ">":
		return boolVal(li > ri), nil
	case "<":
		return boolVal(li < ri), nil
	case "==":
		return boolVal(li == ri), nil
	case "!=":
		return boolVal(li != ri), nil
	}
	return dslValue{}, fmt.Errorf("unknown op %q", op)
}

// parseTerm handles integer literals and function calls.
func (p *dslParser) parseTerm(log *event.Log) (dslValue, error) {
	p.skipWS()
	if p.pos >= len(p.src) {
		return dslValue{}, fmt.Errorf("unexpected end of expression")
	}

	// Integer literal?
	if unicode.IsDigit(rune(p.src[p.pos])) {
		return p.parseInt()
	}

	// Identifier / function call
	ident := p.readIdent()
	if ident == "" {
		return dslValue{}, fmt.Errorf("unexpected character %q", string(p.src[p.pos]))
	}
	p.skipWS()
	if p.pos < len(p.src) && p.src[p.pos] == '(' {
		return p.parseFuncCall(ident, log)
	}
	return dslValue{}, fmt.Errorf("unknown identifier %q (expected a function call)", ident)
}

func (p *dslParser) parseInt() (dslValue, error) {
	start := p.pos
	for p.pos < len(p.src) && unicode.IsDigit(rune(p.src[p.pos])) {
		p.pos++
	}
	n, err := strconv.Atoi(p.src[start:p.pos])
	if err != nil {
		return dslValue{}, err
	}
	return intVal(n), nil
}

func (p *dslParser) readIdent() string {
	start := p.pos
	for p.pos < len(p.src) && (unicode.IsLetter(rune(p.src[p.pos])) || unicode.IsDigit(rune(p.src[p.pos])) || p.src[p.pos] == '_') {
		p.pos++
	}
	return p.src[start:p.pos]
}

// parseFuncCall dispatches to the named function.
func (p *dslParser) parseFuncCall(name string, log *event.Log) (dslValue, error) {
	p.pos++ // consume '('
	switch strings.ToLower(name) {
	case "count":
		return p.evalCount(log)
	default:
		return dslValue{}, fmt.Errorf("unknown function %q", name)
	}
}

// evalCount handles count(kind) and count(kind where field==value).
func (p *dslParser) evalCount(log *event.Log) (dslValue, error) {
	p.skipWS()
	kindStr := p.readIdent()
	if kindStr == "" {
		return dslValue{}, fmt.Errorf("count(): expected event kind")
	}

	// Optional where-clause.
	var field, val string
	if p.tryKeyword("where") {
		p.skipWS()
		f := p.readIdent()
		if f == "" {
			return dslValue{}, fmt.Errorf("count(... where): expected field name")
		}
		p.skipWS()
		if !p.tryOp("==") {
			return dslValue{}, fmt.Errorf("count(... where %s): expected ==", f)
		}
		p.skipWS()
		v := p.readIdent()
		if v == "" {
			return dslValue{}, fmt.Errorf("count(... where %s ==): expected value", f)
		}
		field, val = f, v
	}

	p.skipWS()
	if p.pos >= len(p.src) || p.src[p.pos] != ')' {
		return dslValue{}, fmt.Errorf("count(): expected ')'")
	}
	p.pos++

	// Evaluate against the log.
	k := event.Kind(kindStr)
	events := log.Filter(k)
	if field == "" {
		return intVal(len(events)), nil
	}

	n := 0
	for _, e := range events {
		if matchField(e, field, val) {
			n++
		}
	}
	return intVal(n), nil
}

// matchField returns true when event field equals val.
func matchField(e event.Event, field, val string) bool {
	switch strings.ToLower(field) {
	case "tool":
		return e.Tool == val
	case "method":
		return e.Method == val
	case "action":
		return e.Action == val
	case "key":
		return e.Key == val
	case "source":
		return e.Source == val
	case "direction":
		return e.Direction == val
	}
	return false
}

// ---------------------------------------------------------------------------
// peek helper (unused at package level but useful for debugging)
// ---------------------------------------------------------------------------

func (p *dslParser) remaining() string {
	if p.pos >= len(p.src) {
		return ""
	}
	return p.peek()
}

var _ = (*dslParser).remaining // suppress unused warning
