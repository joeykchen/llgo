/*
 * Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package cl

import (
	"go/token"
	"slices"

	llabi "github.com/xgo-dev/llgo/internal/abi"
	"golang.org/x/tools/go/ssa"
)

const maxStringConcatParts = 16

type stringConcatPlan struct {
	parts         []ssa.Value
	intermediates []*ssa.BinOp // Omitted during lowering; the Go SSA stays unchanged.
}

// A root maps to its ordered operands. A present entry with nil operands is an
// intermediate consumed by that root. All other instructions lower normally.
type stringConcatPlans map[*ssa.BinOp][]ssa.Value

func (plans stringConcatPlans) omits(add *ssa.BinOp) bool {
	parts, found := plans[add]
	return found && parts == nil
}

func isStringAdd(v *ssa.BinOp) bool {
	return v != nil && v.Op == token.ADD && isStringType(v.Type())
}

// Dynamic parts may be empty. Literal bytes give a lower bound on the result.
func concatMayFitStackBuffer(parts []ssa.Value) bool {
	remaining := llabi.SmallStackBufferSize
	for _, part := range parts {
		literal, _ := constStr(part)
		remaining -= len(literal)
		if remaining < 0 {
			return false
		}
	}
	return true
}

func (p *context) prepareStringConcatPlans(fn *ssa.Function) {
	p.concatPlans = nil
	for _, block := range fn.Blocks {
		for index, instr := range block.Instrs {
			root, ok := instr.(*ssa.BinOp)
			if !ok || !isStringAdd(root) {
				continue
			}
			left, _ := root.X.(*ssa.BinOp)
			right, _ := root.Y.(*ssa.BinOp)
			if (!isStringAdd(left) && !isStringAdd(right)) || concatParent(root) != nil {
				continue
			}
			plan := collectStringConcatPlan(root, index, p.staticInitInstrs)
			if plan == nil || !p.fn.ReserveStringConcatTable(len(plan.parts)) {
				continue
			}
			if p.concatPlans == nil {
				p.concatPlans = make(stringConcatPlans)
			}
			p.concatPlans[root] = plan.parts
			for _, add := range plan.intermediates {
				p.concatPlans[add] = nil
			}
		}
	}
}

// concatParent returns the sole executable string-add user in add's block.
// Shared values and values crossing blocks remain materialized. Deferring to
// this parent keeps oversized single-use chains from being partially fused.
func concatParent(add *ssa.BinOp) *ssa.BinOp {
	parent, _ := singleNonDebugReferrer(add).(*ssa.BinOp)
	if isStringAdd(parent) && parent.Block() == add.Block() {
		return parent
	}
	return nil
}

// collectStringConcatPlan returns nil when the chain cannot be fused. index is
// the root's position in its block; SSA dominance makes the evaluated leaves
// available there. Require contiguous additions apart from DebugRef so that
// delaying the string copies cannot cross other executable instructions.
func collectStringConcatPlan(root *ssa.BinOp, index int, static map[ssa.Instruction]none) *stringConcatPlan {
	if _, skipped := static[root]; skipped {
		return nil
	}
	block := root.Block()
	var plan stringConcatPlan
	var collect func(ssa.Value, *ssa.BinOp) bool
	collect = func(v ssa.Value, parent *ssa.BinOp) bool {
		if instr, ok := v.(ssa.Instruction); ok {
			if _, skipped := static[instr]; skipped {
				return false
			}
		}
		add, _ := v.(*ssa.BinOp)
		// Leaves have already been evaluated and dominate their uses. Only
		// same-block additions with one executable user can be eliminated.
		if !isStringAdd(add) || concatParent(add) != parent {
			plan.parts = append(plan.parts, v)
			return true
		}
		// A binary tree with N leaves has N-2 intermediate additions.
		if len(plan.intermediates) == maxStringConcatParts-2 {
			return false
		}
		plan.intermediates = append(plan.intermediates, add)
		return collect(add.X, add) && collect(add.Y, add)
	}
	if !collect(root.X, root) || !collect(root.Y, root) || len(plan.intermediates) == 0 {
		return nil
	}
	for remaining := len(plan.intermediates); remaining > 0; {
		index--
		if index < 0 {
			return nil
		}
		instr := block.Instrs[index]
		if _, debug := instr.(*ssa.DebugRef); debug {
			continue
		}
		add, ok := instr.(*ssa.BinOp)
		if !ok || !slices.Contains(plan.intermediates, add) {
			return nil
		}
		remaining--
	}
	return &plan
}
