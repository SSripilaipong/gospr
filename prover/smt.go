package prover

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"os/exec"

	"gospr/numtype"
)

// z3Timeout bounds a single obligation. A solver that cannot decide within the
// budget is treated as "not proven" (the build fails closed).
const z3Timeout = 10 * time.Second

// lookPath / z3Binary are package-level seams so tests can simulate a missing
// solver or point at a specific z3.
var (
	lookPath = exec.LookPath
	z3Binary = "z3"
)

// checkGoal discharges one obligation: it asserts the negation of the goal's
// equality under the variables' domain/sign constraints and asks Z3 for a
// model. `unsat` (no counterexample) means proven; anything else fails.
func checkGoal(g goal) error {
	if _, err := lookPath(z3Binary); err != nil {
		return fmt.Errorf("the z3 solver is required to prove CRDT convergence but was not found on PATH; install z3 (https://github.com/Z3Prover/z3)")
	}
	res, err := runZ3(buildScript(g))
	if err != nil {
		return err
	}
	switch res {
	case "unsat":
		return nil // no counterexample exists: proven
	case "sat":
		return fmt.Errorf("a counterexample exists")
	default: // unknown
		return fmt.Errorf("z3 could not decide the obligation")
	}
}

// buildScript renders a goal to an SMT-LIB script. Every variable is declared
// as Real; an Int-domain variable additionally gets an integrality constraint,
// so mixed Int/Rat arithmetic never produces an ill-sorted term. This matches
// the builder's numBin rule (any Rat operand widens the result to Rat).
//
// The runtime computes in exact rationals (math/big.Rat), and Z3's Real sort
// over-approximates ℚ (ℚ ⊆ ℝ). Each obligation is a universally-quantified
// quantifier-free formula proved by refuting its negation, so "no real
// counterexample" (unsat) implies "no rational counterexample" — proving over
// Real is sound for the ℚ runtime.
func buildScript(g goal) string {
	var b strings.Builder
	b.WriteString("(set-logic ALL)\n")
	for _, d := range g.vars {
		fmt.Fprintf(&b, "(declare-const %s Real)\n", d.name)
		if d.typ.Domain == numtype.DInt {
			fmt.Fprintf(&b, "(assert (is_int %s))\n", d.name)
		}
		switch d.typ.Sign {
		case numtype.SNonNeg:
			fmt.Fprintf(&b, "(assert (>= %s 0.0))\n", d.name)
		case numtype.SNonPos:
			fmt.Fprintf(&b, "(assert (<= %s 0.0))\n", d.name)
		case numtype.SZero:
			fmt.Fprintf(&b, "(assert (= %s 0.0))\n", d.name)
		}
	}
	fmt.Fprintf(&b, "(assert (not (= %s %s)))\n", serialize(g.lhs), serialize(g.rhs))
	b.WriteString("(check-sat)\n")
	return b.String()
}

// serialize renders a sym to an SMT-LIB s-expression. max/min become ite over a
// comparison; == / /= become = / distinct.
func serialize(s sym) string {
	switch s.kind {
	case symConst:
		return fmtRat(s.num)
	case symVar:
		return s.name
	case symBin:
		a, bb := serialize(*s.a), serialize(*s.b)
		switch s.op {
		case "max":
			return fmt.Sprintf("(ite (>= %s %s) %s %s)", a, bb, a, bb)
		case "min":
			return fmt.Sprintf("(ite (<= %s %s) %s %s)", a, bb, a, bb)
		default: // + - *
			return fmt.Sprintf("(%s %s %s)", s.op, a, bb)
		}
	case symCmp:
		a, bb := serialize(*s.a), serialize(*s.b)
		switch s.op {
		case "==":
			return fmt.Sprintf("(= %s %s)", a, bb)
		case "/=":
			return fmt.Sprintf("(distinct %s %s)", a, bb)
		default: // > < >= <=
			return fmt.Sprintf("(%s %s %s)", s.op, a, bb)
		}
	case symIte:
		return fmt.Sprintf("(ite %s %s %s)", serialize(*s.cond), serialize(*s.a), serialize(*s.b))
	default:
		return "0.0"
	}
}

// fmtRat renders an exact rational as an SMT-LIB Real literal. An integer p
// becomes "p.0"; a fraction p/q becomes "(/ p.0 q.0)" (Z3 reads this as the
// exact rational). Negatives go through unary minus, which SMT-LIB requires.
func fmtRat(r *big.Rat) string {
	if r.Sign() < 0 {
		return "(- " + fmtRat(new(big.Rat).Neg(r)) + ")"
	}
	num := r.Num().String() + ".0"
	if r.IsInt() {
		return num
	}
	return "(/ " + num + " " + r.Denom().String() + ".0)"
}

// runZ3 feeds the script to `z3 -smt2 -in` and returns its first result token
// (sat/unsat/unknown).
func runZ3(script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), z3Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, z3Binary, "-smt2", "-in")
	cmd.Stdin = strings.NewReader(script)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	runErr := cmd.Run()

	if res := firstToken(out.String()); res == "sat" || res == "unsat" || res == "unknown" {
		return res, nil
	}
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("z3 timed out after %s", z3Timeout)
	}
	return "", fmt.Errorf("unexpected z3 output %q (stderr: %s): %v", strings.TrimSpace(out.String()), strings.TrimSpace(errb.String()), runErr)
}

func firstToken(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}
