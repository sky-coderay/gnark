// Copyright 2020 ConsenSys AG
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Code generated by gnark/internal/generators DO NOT EDIT

package groth16

import (
	"math/big"

	curve "github.com/consensys/gurvy/bls377"
	"github.com/consensys/gurvy/bls377/fr"

	bls377backend "github.com/consensys/gnark/internal/backend/bls377"

	"runtime"
	"sync"

	"github.com/consensys/gnark/internal/utils/debug"
)

// Proof represents a Groth16 proof that was encoded with a ProvingKey and can be verified
// with a valid statement and a VerifyingKey
type Proof struct {
	Ar, Krs curve.G1Affine
	Bs      curve.G2Affine
}

// Prove creates proof from a circuit
func Prove(r1cs *bls377backend.R1CS, pk *ProvingKey, solution map[string]interface{}) (*Proof, error) {
	nbPrivateWires := r1cs.NbWires - r1cs.NbPublicWires

	// fft domain (computeH)
	fftDomain := bls377backend.NewDomain(r1cs.NbConstraints)

	// solve the R1CS and compute the a, b, c vectors
	a := make([]fr.Element, r1cs.NbConstraints, fftDomain.Cardinality)
	b := make([]fr.Element, r1cs.NbConstraints, fftDomain.Cardinality)
	c := make([]fr.Element, r1cs.NbConstraints, fftDomain.Cardinality)
	wireValues := make([]fr.Element, r1cs.NbWires)
	if err := r1cs.Solve(solution, a, b, c, wireValues); err != nil {
		return nil, err
	}

	// set the wire values in regular form
	execute(len(wireValues), func(start, end int) {
		for i := start; i < end; i++ {
			wireValues[i].FromMont()
		}
	})

	// H (witness reduction / FFT part)
	var h []fr.Element
	chHDone := make(chan struct{}, 1)
	go func() {
		h = computeH(a, b, c, fftDomain)
		a = nil
		b = nil
		c = nil
		chHDone <- struct{}{}
	}()

	// sample random r and s
	var r, s big.Int
	var _r, _s, _kr fr.Element
	_r.SetRandom()
	_s.SetRandom()
	_kr.Mul(&_r, &_s).Neg(&_kr)

	_r.FromMont()
	_s.FromMont()
	_kr.FromMont()
	_r.ToBigInt(&r)
	_s.ToBigInt(&s)

	// computes r[δ], s[δ], kr[δ]
	deltas := curve.BatchScalarMultiplicationG1(&pk.G1.Delta, []fr.Element{_r, _s, _kr})

	// wait for FFT to end, as it uses all our CPUs
	<-chHDone

	proof := &Proof{}
	var bs1, ar curve.G1Jac

	// using this ensures that our multiExps running in parallel won't use more than
	// provided CPUs
	opt := curve.NewMultiExpOptions(runtime.NumCPU())

	chBs1Done := make(chan struct{}, 1)
	computeBS1 := func() {
		bs1.MultiExp(pk.G1.B, wireValues, opt)
		bs1.AddMixed(&pk.G1.Beta)
		bs1.AddMixed(&deltas[1])
		chBs1Done <- struct{}{}
	}

	chArDone := make(chan struct{}, 1)
	computeAR1 := func() {
		ar.MultiExp(pk.G1.A, wireValues, opt)
		ar.AddMixed(&pk.G1.Alpha)
		ar.AddMixed(&deltas[0])
		proof.Ar.FromJacobian(&ar)
		chArDone <- struct{}{}
	}

	chKrsDone := make(chan struct{}, 1)
	computeKRS := func() {
		// we could NOT split the Krs multiExp in 2, and just append pk.G1.K and pk.G1.Z
		// however, having similar lengths for our tasks helps with parallelism

		var krs, krs2, p1 curve.G1Jac
		chKrs2Done := make(chan struct{}, 1)
		go func() {
			krs2.MultiExp(pk.G1.Z, h, opt)
			chKrs2Done <- struct{}{}
		}()
		krs.MultiExp(pk.G1.K[:nbPrivateWires], wireValues[:nbPrivateWires], opt)
		krs.AddMixed(&deltas[2])
		n := 3
		for n != 0 {
			select {
			case <-chKrs2Done:
				krs.AddAssign(&krs2)
			case <-chArDone:
				p1.ScalarMulGLV(&ar, &s)
				krs.AddAssign(&p1)
			case <-chBs1Done:
				p1.ScalarMulGLV(&bs1, &r)
				krs.AddAssign(&p1)
			}
			n--
		}

		proof.Krs.FromJacobian(&krs)
		chKrsDone <- struct{}{}
	}

	// schedule our proof part computations
	go computeKRS()
	go computeAR1()
	go computeBS1()

	{
		// Bs2 (1 multi exp G2 - size = len(wires))
		var Bs, deltaS curve.G2Jac

		// splitting Bs2 in 3 ensures all our go routines in the prover have similar running time
		// and is good for parallelism. However, on a machine with limited CPUs, this may not be
		// a good idea, as the MultiExp scales slightly better than linearly
		bsSplit := len(pk.G2.B) / 3
		if bsSplit > 10 {
			chDone1 := make(chan struct{}, 1)
			chDone2 := make(chan struct{}, 1)
			var bs1, bs2 curve.G2Jac
			go func() {
				bs1.MultiExp(pk.G2.B[:bsSplit], wireValues[:bsSplit], opt)
				chDone1 <- struct{}{}
			}()
			go func() {
				bs2.MultiExp(pk.G2.B[bsSplit:bsSplit*2], wireValues[bsSplit:bsSplit*2], opt)
				chDone2 <- struct{}{}
			}()
			Bs.MultiExp(pk.G2.B[bsSplit*2:], wireValues[bsSplit*2:], opt)

			<-chDone1
			Bs.AddAssign(&bs1)
			<-chDone2
			Bs.AddAssign(&bs2)
		} else {
			Bs.MultiExp(pk.G2.B, wireValues, opt)
		}

		deltaS.FromAffine(&pk.G2.Delta)
		deltaS.ScalarMulGLV(&deltaS, &s)
		Bs.AddAssign(&deltaS)
		Bs.AddMixed(&pk.G2.Beta)

		proof.Bs.FromJacobian(&Bs)
	}

	// wait for all parts of the proof to be computed.
	<-chKrsDone

	return proof, nil
}

func computeH(a, b, c []fr.Element, fftDomain *bls377backend.Domain) []fr.Element {
	// H part of Krs
	// Compute H (hz=ab-c, where z=-2 on ker X^n+1 (z(x)=x^n-1))
	// 	1 - _a = ifft(a), _b = ifft(b), _c = ifft(c)
	// 	2 - ca = fft_coset(_a), ba = fft_coset(_b), cc = fft_coset(_c)
	// 	3 - h = ifft_coset(ca o cb - cc)

	n := len(a)
	debug.Assert((n == len(b)) && (n == len(c)))

	// add padding
	padding := make([]fr.Element, fftDomain.Cardinality-n)
	a = append(a, padding...)
	b = append(b, padding...)
	c = append(c, padding...)
	n = len(a)

	// exptable = scale by inverse of n + coset
	// ifft(a) would normaly do FFT(a, wInv) then scale by CardinalityInv
	// fft_coset(a) would normaly mutliply a with expTable of fftDomain.GeneratorSqRt
	// this pre-computed expTable do both in one pass --> it contains
	// expTable[0] = fftDomain.CardinalityInv
	// expTable[1] = fftDomain.GeneratorSqrt^1 * fftDomain.CardinalityInv
	// expTable[2] = fftDomain.GeneratorSqrt^2 * fftDomain.CardinalityInv
	// ...
	expTable := make([]fr.Element, n)
	expTable[0] = fftDomain.CardinalityInv

	var wgExpTable sync.WaitGroup

	// to ensure the pool is busy while the FFT splits, we schedule precomputation of the exp table
	// before the FFTs
	asyncExpTable(fftDomain.CardinalityInv, fftDomain.GeneratorSqRt, expTable, &wgExpTable)

	var wg sync.WaitGroup
	FFTa := func(s []fr.Element) {
		// FFT inverse
		bls377backend.FFT(s, fftDomain.GeneratorInv)

		// wait for the expTable to be pre-computed
		// in the nominal case, this is non-blocking as the expTable was scheduled before the FFT
		wgExpTable.Wait()
		execute(n, func(start, end int) {
			for i := start; i < end; i++ {
				s[i].Mul(&s[i], &expTable[i])
			}
		})

		// FFT coset
		bls377backend.FFT(s, fftDomain.Generator)
		wg.Done()
	}
	wg.Add(3)
	go FFTa(a)
	go FFTa(b)
	FFTa(c)

	var minusTwoInv fr.Element
	minusTwoInv.SetUint64(2)
	minusTwoInv.Neg(&minusTwoInv).
		Inverse(&minusTwoInv)

	// wait for first step (ifft + fft_coset) to be done
	wg.Wait()

	// h = ifft_coset(ca o cb - cc)
	// reusing a to avoid unecessary memalloc
	execute(n, func(start, end int) {
		for i := start; i < end; i++ {
			a[i].Mul(&a[i], &b[i]).
				Sub(&a[i], &c[i]).
				Mul(&a[i], &minusTwoInv)
		}
	})

	// before computing the ifft_coset, we schedule the expTable precompute of the ifft_coset
	// to ensure the pool is busy while the FFT splits
	// similar reasoning as in ifft pass -->
	// expTable[0] = fftDomain.CardinalityInv
	// expTable[1] = fftDomain.GeneratorSqRtInv^1 * fftDomain.CardinalityInv
	// expTable[2] = fftDomain.GeneratorSqRtInv^2 * fftDomain.CardinalityInv
	asyncExpTable(fftDomain.CardinalityInv, fftDomain.GeneratorSqRtInv, expTable, &wgExpTable)

	// ifft_coset
	bls377backend.FFT(a, fftDomain.GeneratorInv)

	wgExpTable.Wait() // wait for pre-computation of exp table to be done
	execute(n, func(start, end int) {
		for i := start; i < end; i++ {
			a[i].Mul(&a[i], &expTable[i]).FromMont()
		}
	})

	return a
}

func asyncExpTable(scale, w fr.Element, table []fr.Element, wg *sync.WaitGroup) {
	n := len(table)

	// see if it makes sense to parallelize exp tables pre-computation
	interval := (n - 1) / runtime.NumCPU()
	// this ratio roughly correspond to the number of multiplication one can do in place of a Exp operation
	const ratioExpMul = 2400 / 26

	if interval < ratioExpMul {
		wg.Add(1)
		go func() {
			precomputeExpTableChunk(scale, w, 1, table[1:])
			wg.Done()
		}()
	} else {
		// we parallelize
		for i := 1; i < n; i += interval {
			start := i
			end := i + interval
			if end > n {
				end = n
			}
			wg.Add(1)
			go func() {
				precomputeExpTableChunk(scale, w, uint64(start), table[start:end])
				wg.Done()
			}()
		}
	}
}

func precomputeExpTableChunk(scale, w fr.Element, power uint64, table []fr.Element) {
	table[0].Exp(w, new(big.Int).SetUint64(power))
	table[0].Mul(&table[0], &scale)
	for i := 1; i < len(table); i++ {
		table[i].Mul(&table[i-1], &w)
	}
}

// execute process in parallel the work function, using all available CPUs
func execute(nbIterations int, work func(int, int)) {

	nbTasks := runtime.NumCPU()
	nbIterationsPerCpus := nbIterations / nbTasks

	// more CPUs than tasks: a CPU will work on exactly one iteration
	if nbIterationsPerCpus < 1 {
		nbIterationsPerCpus = 1
		nbTasks = nbIterations
	}

	var wg sync.WaitGroup

	extraTasks := nbIterations - (nbTasks * nbIterationsPerCpus)
	extraTasksOffset := 0

	for i := 0; i < nbTasks; i++ {
		wg.Add(1)
		_start := i*nbIterationsPerCpus + extraTasksOffset
		_end := _start + nbIterationsPerCpus
		if extraTasks > 0 {
			_end++
			extraTasks--
			extraTasksOffset++
		}
		go func() {
			work(_start, _end)
			wg.Done()
		}()
	}

	wg.Wait()
}
