// Copyright 2021 Matrix Origin
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package anti

import (
	"github.com/matrixorigin/matrixone/pkg/common/hashmap"
	"github.com/matrixorigin/matrixone/pkg/common/reuse"
	"github.com/matrixorigin/matrixone/pkg/container/batch"
	"github.com/matrixorigin/matrixone/pkg/container/types"
	"github.com/matrixorigin/matrixone/pkg/container/vector"
	"github.com/matrixorigin/matrixone/pkg/sql/colexec"
	"github.com/matrixorigin/matrixone/pkg/sql/plan"
	"github.com/matrixorigin/matrixone/pkg/vm"
	"github.com/matrixorigin/matrixone/pkg/vm/process"
)

var _ vm.Operator = new(Argument)

const (
	Build = iota
	Probe
	End
)

type container struct {
	colexec.ReceiverOperator

	state int

	hasNull bool

	inBuckets []uint8

	batches       []*batch.Batch
	batchRowCount int
	rbat          *batch.Batch

	expr colexec.ExpressionExecutor

	joinBat1 *batch.Batch
	cfs1     []func(*vector.Vector, *vector.Vector, int64, int) error

	joinBat2 *batch.Batch
	cfs2     []func(*vector.Vector, *vector.Vector, int64, int) error

	executorForVecs []colexec.ExpressionExecutor
	vecs            []*vector.Vector

	mp *hashmap.JoinMap

	maxAllocSize int64
}

type Argument struct {
	ctr        *container
	Result     []int32
	Typs       []types.Type
	Cond       *plan.Expr
	Conditions [][]*plan.Expr
	HashOnPK   bool
	IsShuffle  bool
	bat        *batch.Batch
	lastrow    int

	vm.OperatorBase
}

func (arg *Argument) GetOperatorBase() *vm.OperatorBase {
	return &arg.OperatorBase
}

func init() {
	reuse.CreatePool[Argument](
		func() *Argument {
			return &Argument{}
		},
		func(a *Argument) {
			*a = Argument{}
		},
		reuse.DefaultOptions[Argument]().
			WithEnableChecker(),
	)
}

func (arg Argument) TypeName() string {
	return argName
}

func NewArgument() *Argument {
	return reuse.Alloc[Argument](nil)
}

func (arg *Argument) Release() {
	if arg != nil {
		reuse.Free[Argument](arg, nil)
	}
}

func (arg *Argument) Reset(proc *process.Process, pipelineFailed bool, err error) {
	ctr := arg.ctr
	if ctr != nil {
		ctr.cleanBatch(proc)
		ctr.cleanHashMap()
		ctr.FreeAllReg()

		ctr.state = Build
		ctr.hasNull = false
		ctr.batchRowCount = 0

		anal := proc.GetAnalyze(arg.GetIdx(), arg.GetParallelIdx(), arg.GetParallelMajor())
		anal.Alloc(ctr.maxAllocSize)
		ctr.maxAllocSize = 0
	}
	arg.cleanBatch(proc)
	arg.lastrow = 0
}

func (arg *Argument) Free(proc *process.Process, pipelineFailed bool, err error) {
	ctr := arg.ctr
	if ctr != nil {
		ctr.cleanBatch(proc)
		ctr.cleanEvalVectors()
		ctr.cleanHashMap()
		ctr.cleanExprExecutor()
		ctr.FreeAllReg()

		anal := proc.GetAnalyze(arg.GetIdx(), arg.GetParallelIdx(), arg.GetParallelMajor())
		anal.Alloc(ctr.maxAllocSize)
	}
	arg.cleanBatch(proc)
}

func (arg *Argument) cleanBatch(proc *process.Process) {
	if arg.bat != nil {
		arg.bat.Clean(proc.GetMPool())
		arg.bat = nil
	}
}

func (ctr *container) cleanExprExecutor() {
	if ctr.expr != nil {
		ctr.expr.Free()
		ctr.expr = nil
	}
}

func (ctr *container) cleanBatch(proc *process.Process) {
	for i := range ctr.batches {
		proc.PutBatch(ctr.batches[i])
	}
	ctr.batches = nil
	if ctr.rbat != nil {
		proc.PutBatch(ctr.rbat)
		ctr.rbat = nil
	}
	if ctr.joinBat1 != nil {
		proc.PutBatch(ctr.joinBat1)
		ctr.joinBat1 = nil
	}
	if ctr.joinBat2 != nil {
		proc.PutBatch(ctr.joinBat2)
		ctr.joinBat2 = nil
	}
}

func (ctr *container) cleanHashMap() {
	if ctr.mp != nil {
		ctr.mp.Free()
		ctr.mp = nil
	}
}

func (ctr *container) cleanEvalVectors() {
	for i := range ctr.executorForVecs {
		if ctr.executorForVecs[i] != nil {
			ctr.executorForVecs[i].Free()
		}
	}
	ctr.executorForVecs = nil
}
