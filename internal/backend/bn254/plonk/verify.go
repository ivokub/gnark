// Copyright 2020 ConsenSys Software Inc.
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

package plonk

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr/kzg"

	curve "github.com/consensys/gnark-crypto/ecc/bn254"

	bn254witness "github.com/consensys/gnark/internal/backend/bn254/witness"

	"github.com/consensys/gnark-crypto/ecc"
	fiatshamir "github.com/consensys/gnark-crypto/fiat-shamir"
)

var (
	errWrongClaimedQuotient = errors.New("claimed quotient is not as expected")
)

func Verify(proof *Proof, vk *VerifyingKey, publicWitness bn254witness.Witness) error {

	// pick a hash function to derive the challenge (the same as in the prover)
	hFunc := sha256.New()

	// transcript to derive the challenge
	fs := fiatshamir.NewTranscript(hFunc, "gamma", "alpha", "zeta")

	// derive gamma from Comm(l), Comm(r), Comm(o)
	gamma, err := deriveRandomness(&fs, "gamma", &proof.LRO[0], &proof.LRO[1], &proof.LRO[2])
	if err != nil {
		return err
	}

	// derive alpha from Comm(l), Comm(r), Comm(o), Com(Z)
	alpha, err := deriveRandomness(&fs, "alpha", &proof.Z)
	if err != nil {
		return err
	}

	// derive zeta, the point of evaluation
	zeta, err := deriveRandomness(&fs, "zeta", &proof.H[0], &proof.H[1], &proof.H[2])
	if err != nil {
		return err
	}

	// evaluation of Z=Xⁿ⁻¹ at ζ
	var zetaPowerM, zzeta fr.Element
	var bExpo big.Int
	one := fr.One()
	bExpo.SetUint64(vk.Size)
	zetaPowerM.Exp(zeta, &bExpo)
	zzeta.Sub(&zetaPowerM, &one)

	// ccompute PI = ∑_{i<n} Lᵢ*wᵢ
	// TODO use batch inversion
	var pi, den, lagrangeOne, xiLi fr.Element
	lagrange := zzeta // ζⁿ⁻¹
	acc := fr.One()
	den.Sub(&zeta, &acc)
	lagrange.Div(&lagrange, &den).Mul(&lagrange, &vk.SizeInv) // (1/n)*(ζⁿ⁻¹)/(ζ-1)
	lagrangeOne.Set(&lagrange)                                // save it for later
	for i := 0; i < len(publicWitness); i++ {

		xiLi.Mul(&lagrange, &publicWitness[i])
		pi.Add(&pi, &xiLi)

		// use Lᵢ₊₁ = w*L_i*(X-z^{i})/(X-zⁱ⁺¹)
		lagrange.Mul(&lagrange, &vk.Generator).
			Mul(&lagrange, &den)
		acc.Mul(&acc, &vk.Generator)
		den.Sub(&zeta, &acc)
		lagrange.Div(&lagrange, &den)
	}

	// linearizedpolynomial + pi(ζ) + α*(Z(μζ))*(l(ζ)+\beta*s1(ζ)+γ)*(r(ζ)+\beta*s2(ζ)+γ)*(o(ζ)+γ) - α²*L₁(ζ)
	var _s1, _s2, _o, alphaSquareLagrange fr.Element

	zu := proof.ZShiftedOpening.ClaimedValue

	claimedQuotient := proof.BatchedProof.ClaimedValues[0]          // CORRECT
	linearizedPolynomialZeta := proof.BatchedProof.ClaimedValues[1] // CORRECT
	l := proof.BatchedProof.ClaimedValues[2]                        // CORRECT
	r := proof.BatchedProof.ClaimedValues[3]                        // CORRECT
	o := proof.BatchedProof.ClaimedValues[4]                        // CORRECT
	s1 := proof.BatchedProof.ClaimedValues[5]                       // CORRECT
	s2 := proof.BatchedProof.ClaimedValues[6]                       // CORRECT

	fmt.Printf("h(zeta) = %s\n", claimedQuotient.String())

	var beta fr.Element
	beta.SetUint64(10)

	_s1.Mul(&s1, &beta).Add(&_s1, &l).Add(&_s1, &gamma) // (l(ζ)+\beta*s1(ζ)+γ)
	_s2.Mul(&s2, &beta).Add(&_s2, &r).Add(&_s2, &gamma) // (r(ζ)+\beta*s2(ζ)+γ)
	_o.Add(&o, &gamma)                                  // (o(ζ)+γ)

	_s1.Mul(&_s1, &_s2).
		Mul(&_s1, &_o).
		Mul(&_s1, &alpha).
		Mul(&_s1, &zu) //  α*(Z(μζ))*(l(ζ)+\beta*s1(ζ)+γ)*(r(ζ)+\beta*s2(ζ)+γ)*(o(ζ)+γ)

	fmt.Printf("α*(Z(μζ))*(l(ζ)+s1(ζ)+γ)*(r(ζ)+s2(ζ)+γ)*(o(ζ)+γ) = %s\n", _s1.String())

	alphaSquareLagrange.Mul(&lagrangeOne, &alpha).
		Mul(&alphaSquareLagrange, &alpha) // α²*L₁(ζ)

	linearizedPolynomialZeta.
		Add(&linearizedPolynomialZeta, &pi).                 // linearizedpolynomial + pi(zeta)
		Add(&linearizedPolynomialZeta, &_s1).                // linearizedpolynomial+pi(zeta)+α*(Z(μζ))*(l(ζ)+s1(ζ)+γ)*(r(ζ)+s2(ζ)+γ)*(o(ζ)+γ)
		Sub(&linearizedPolynomialZeta, &alphaSquareLagrange) // linearizedpolynomial+pi(zeta)+α*(Z(μζ))*(l(ζ)+s1(ζ)+γ)*(r(ζ)+s2(ζ)+γ)*(o(ζ)+γ)-α²*L₁(ζ)

	fmt.Printf("linpolcompleted(zeta) = %s\n", linearizedPolynomialZeta.String())

	// Compute H(ζ) using the previous result: H(ζ) = prev_result/(ζⁿ-1)
	var zetaPowerMMinusOne fr.Element
	zetaPowerMMinusOne.Sub(&zetaPowerM, &one)
	linearizedPolynomialZeta.Div(&linearizedPolynomialZeta, &zetaPowerMMinusOne)

	// check that H(ζ) is as claimed
	if !claimedQuotient.Equal(&linearizedPolynomialZeta) {
		return errWrongClaimedQuotient
	}

	// compute the folded commitment to H: Comm(h₁) + ζᵐ*Comm(h₂) + ζ²ᵐ*Comm(h₃)
	mPlusTwo := big.NewInt(int64(vk.Size) + 2)
	var zetaMPlusTwo fr.Element
	zetaMPlusTwo.Exp(zeta, mPlusTwo)
	var zetaMPlusTwoBigInt big.Int
	zetaMPlusTwo.ToBigIntRegular(&zetaMPlusTwoBigInt)
	foldedH := proof.H[2]
	foldedH.ScalarMultiplication(&foldedH, &zetaMPlusTwoBigInt)
	foldedH.Add(&foldedH, &proof.H[1])
	foldedH.ScalarMultiplication(&foldedH, &zetaMPlusTwoBigInt)
	foldedH.Add(&foldedH, &proof.H[0])

	// Compute the commitment to the linearized polynomial
	// linearizedPolynomialDigest =
	// 		l(ζ)*ql+r(ζ)*qr+r(ζ)l(ζ)*qm+o(ζ)*qo+qk +
	// 		α*( Z(μζ)(l(ζ)+β*s₁(ζ)+γ)*(r(ζ)+β*s₂(ζ)+γ)*s₃(X)-Z(X)(l(ζ)+β*id_1(ζ)+γ)*(r(ζ)+β*id_2(ζ)+γ)*(o(ζ)+β*id_3(ζ)+γ) ) +
	// 		α²*L₁(ζ)*Z
	// first part: individual constraints
	var rl fr.Element
	rl.Mul(&l, &r)

	var linearizedPolynomialDigest curve.G1Affine

	// second part: α*( Z(μζ)(l(ζ)+β*s₁(ζ)+γ)*(r(ζ)+β*s₂(ζ)+γ)*s₃(X)-Z(X)(l(ζ)+β*id_1(ζ)+γ)*(r(ζ)+β*id_2(ζ)+γ)*(o(ζ)+β*id_3(ζ)+γ) ) )
	var t fr.Element
	_s1.Mul(&s1, &beta).Add(&_s1, &l).Add(&_s1, &gamma)
	t.Mul(&s2, &beta).Add(&t, &t).Add(&t, &gamma)
	_s1.Mul(&_s1, &t).
		Mul(&_s1, &zu).
		Mul(&_s1, &alpha) // α*( Z(μζ)(l(ζ)+β*s₁(ζ)+γ)*(r(ζ)+β*s₂(ζ)+γ)

	var cosetShift, cosetShiftSquare fr.Element
	cosetShift.Set(&vk.CosetShift)
	cosetShiftSquare.Square(&cosetShift)
	_s2.Mul(&beta, &zeta).Add(&_s2, &l).Add(&_s2, &gamma)                   // (l(ζ)+β*ζ+γ)
	t.Mul(&zeta, &cosetShift).Mul(&t, &zeta).Add(&t, &r).Add(&t, &gamma)    // (r(ζ)+β*u*ζ+γ)
	_s2.Mul(&_s2, &t)                                                       // (l(ζ)+β*ζ+γ)*(r(ζ)+β*u*ζ+γ)
	t.Mul(&t, &cosetShiftSquare).Mul(&t, &zeta).Add(&t, &o).Add(&t, &gamma) // (o(ζ)+β*u²*ζ+γ)
	_s2.Mul(&_s2, &t)                                                       // (l(ζ)+β*ζ+γ)*(r(ζ)+β*u*ζ+γ)*(o(ζ)+β*u²*ζ+γ)
	_s2.Mul(&_s2, &alpha)
	_s2.Sub(&alphaSquareLagrange, &_s2)

	// note since third part =  α²*L₁(ζ)*Z
	// we add alphaSquareLagrange to _s2

	points := []curve.G1Affine{
		vk.Ql, vk.Qr, vk.Qm, vk.Qo, vk.Qk, // first part
		vk.S[2], proof.Z, // second & third part
	}

	scalars := []fr.Element{
		l, r, rl, o, one, // first part
		_s1, _s2, // second & third part
	}
	if _, err := linearizedPolynomialDigest.MultiExp(points, scalars, ecc.MultiExpConfig{ScalarsMont: true}); err != nil {
		return err
	}

	// Fold the first proof
	foldedProof, foldedDigest, err := kzg.FoldProof([]kzg.Digest{
		foldedH,
		linearizedPolynomialDigest,
		proof.LRO[0],
		proof.LRO[1],
		proof.LRO[2],
		vk.S[0],
		vk.S[1],
	},
		&proof.BatchedProof,
		hFunc,
	)
	if err != nil {
		return err
	}

	// Batch verify
	return kzg.BatchVerifyMultiPoints([]kzg.Digest{
		foldedDigest,
		proof.Z,
	},
		[]kzg.OpeningProof{
			foldedProof,
			proof.ZShiftedOpening,
		},
		vk.KZGSRS,
	)
}

func deriveRandomness(fs *fiatshamir.Transcript, challenge string, points ...*curve.G1Affine) (fr.Element, error) {

	var buf [curve.SizeOfG1AffineUncompressed]byte
	var r fr.Element

	for _, p := range points {
		buf = p.RawBytes()
		if err := fs.Bind(challenge, buf[:]); err != nil {
			return r, err
		}
	}

	b, err := fs.ComputeChallenge(challenge)
	if err != nil {
		return r, err
	}
	r.SetBytes(b)
	return r, nil
}
