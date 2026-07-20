# AgentChaos Assertion DSL Grammar

Use assertion type `"expr"` and set the `expr` field to a DSL expression:

```yaml
assertions:
  - type: expr
    expr: "count(response_delivered) >= 1"
  - type: expr
    expr: "count(fault_fired where action==duplicate) == 0 or count(terminal_state) >= 1"
```

## Grammar (EBNF)

```ebnf
expr       = or ;
or         = and ( "or" and )* ;
and        = not ( "and" not )* ;
not        = "not" primary | primary ;
primary    = "(" expr ")" | comparison ;
comparison = term ( cmpOp term )? ;
term       = int_literal | func_call ;
func_call  = IDENT "(" arglist? ")" ;
arglist    = kind [ "where" field "==" value ] ;
kind       = IDENT ;   (* event.Kind string, e.g. response_delivered *)
field      = IDENT ;   (* tool | method | action | key | source | direction *)
value      = IDENT ;
cmpOp      = ">=" | "<=" | ">" | "<" | "==" | "!=" ;
int_literal = DIGIT+ ;
IDENT      = LETTER ( LETTER | DIGIT | "_" )* ;
```

## Functions

| Function | Returns | Description |
|---|---|---|
| `count(kind)` | int | Number of events with the given kind |
| `count(kind where field==value)` | int | Filtered count |

## Supported kinds

Any `event.Kind` string: `request_sent`, `response_received`, `response_delivered`,
`notification_sent`, `notification_delivered`, `fault_fired`, `response_dropped`,
`response_duplicated`, `process_killed`, `checkpoint_commit`, `terminal_state`.

## Supported where-clause fields

`tool`, `method`, `action`, `key`, `source`, `direction`

## Boolean operators

`and`, `or`, `not` (case-insensitive)

## Examples

```
count(response_delivered) >= 1
count(fault_fired where action==duplicate) <= 1
count(terminal_state) == 1 and count(checkpoint_commit) >= 1
not (count(response_dropped) > 0)
```
