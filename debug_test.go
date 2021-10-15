package gnark

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/consensys/gnark-crypto/ecc"
	"github.com/consensys/gnark/backend"
	"github.com/consensys/gnark/backend/groth16"
	"github.com/consensys/gnark/backend/plonk"
	"github.com/consensys/gnark/frontend"
	"github.com/consensys/gnark/test"
	"github.com/stretchr/testify/require"
)

// -------------------------------------------------------------------------------------------------
// test println (non regression)
type printlnCircuit struct {
	A, B frontend.Variable
}

func (circuit *printlnCircuit) Define(curveID ecc.ID, cs frontend.API) error {
	c := cs.Add(circuit.A, circuit.B)
	cs.Println(c, "is the addition")
	d := cs.Mul(circuit.A, c)
	cs.Println(d, new(big.Int).SetInt64(42))
	bs := cs.ToBinary(circuit.B, 10)
	cs.Println("bits", bs[3])
	cs.Println("circuit", circuit)
	cs.AssertIsBoolean(cs.Constant(10)) // this will fail
	m := cs.Mul(circuit.A, circuit.B)
	cs.Println("m", m) // this should not be resolved
	return nil
}

func TestPrintln(t *testing.T) {
	assert := require.New(t)

	var circuit, witness printlnCircuit
	witness.A.Assign(2)
	witness.B.Assign(11)

	var expected bytes.Buffer
	expected.WriteString("debug_test.go:25 13 is the addition\n")
	expected.WriteString("debug_test.go:27 26 42\n")
	expected.WriteString("debug_test.go:29 bits 1\n")
	expected.WriteString("debug_test.go:30 circuit {A: 2, B: 11}\n")
	expected.WriteString("debug_test.go:33 m <unsolved>\n")

	{
		trace, _ := getGroth16Trace(&circuit, &witness)
		assert.Equal(trace, expected.String())
	}

	{
		trace, _ := getPlonkTrace(&circuit, &witness)
		assert.Equal(trace, expected.String())
	}
}

// -------------------------------------------------------------------------------------------------
// Div by 0
type divBy0Trace struct {
	A, B, C frontend.Variable
}

func (circuit *divBy0Trace) Define(curveID ecc.ID, cs frontend.API) error {
	d := cs.Add(circuit.B, circuit.C)
	cs.Div(circuit.A, d)
	return nil
}

func TestTraceDivBy0(t *testing.T) {
	assert := require.New(t)

	var circuit, witness divBy0Trace
	witness.A.Assign(2)
	witness.B.Assign(-2)
	witness.C.Assign(2)

	{
		_, err := getGroth16Trace(&circuit, &witness)
		assert.Error(err)
		assert.Contains(err.Error(), "constraint is not satisfied: [div] 2/(-2 + 2) == 0")
		assert.Contains(err.Error(), "gnark.(*divBy0Trace).Define")
		assert.Contains(err.Error(), "debug_test.go:")
	}

	{
		_, err := getPlonkTrace(&circuit, &witness)
		assert.Error(err)
		assert.Contains(err.Error(), "constraint is not satisfied: [div] 2/(-2 + 2) == 0")
		assert.Contains(err.Error(), "gnark.(*divBy0Trace).Define")
		assert.Contains(err.Error(), "debug_test.go:")
	}
}

// -------------------------------------------------------------------------------------------------
// Not Equal
type notEqualTrace struct {
	A, B, C frontend.Variable
}

func (circuit *notEqualTrace) Define(curveID ecc.ID, cs frontend.API) error {
	d := cs.Add(circuit.B, circuit.C)
	cs.AssertIsEqual(circuit.A, d)
	return nil
}

func TestTraceNotEqual(t *testing.T) {
	assert := require.New(t)

	var circuit, witness notEqualTrace
	witness.A.Assign(1)
	witness.B.Assign(24)
	witness.C.Assign(42)

	{
		_, err := getGroth16Trace(&circuit, &witness)
		assert.Error(err)
		assert.Contains(err.Error(), "constraint is not satisfied: [assertIsEqual] 1 == (24 + 42)")
		assert.Contains(err.Error(), "gnark.(*notEqualTrace).Define")
		assert.Contains(err.Error(), "debug_test.go:")
	}

	{
		_, err := getPlonkTrace(&circuit, &witness)
		assert.Error(err)
		assert.Contains(err.Error(), "constraint is not satisfied: [assertIsEqual] 1 == (24 + 42)")
		assert.Contains(err.Error(), "gnark.(*notEqualTrace).Define")
		assert.Contains(err.Error(), "debug_test.go:")
	}
}

// -------------------------------------------------------------------------------------------------
// Not boolean
type notBooleanTrace struct {
	A, B, C frontend.Variable
}

func (circuit *notBooleanTrace) Define(curveID ecc.ID, cs frontend.API) error {
	d := cs.Add(circuit.B, circuit.C)
	cs.AssertIsBoolean(d)
	return nil
}

func TestTraceNotBoolean(t *testing.T) {
	assert := require.New(t)

	var circuit, witness notBooleanTrace
	witness.A.Assign(1)
	witness.B.Assign(24)
	witness.C.Assign(42)

	{
		_, err := getGroth16Trace(&circuit, &witness)
		assert.Error(err)
		assert.Contains(err.Error(), "constraint is not satisfied: [assertIsBoolean] (24 + 42) == (0|1)")
		assert.Contains(err.Error(), "gnark.(*notBooleanTrace).Define")
		assert.Contains(err.Error(), "debug_test.go:")
	}

	{
		_, err := getPlonkTrace(&circuit, &witness)
		assert.Error(err)
		assert.Contains(err.Error(), "constraint is not satisfied: [assertIsBoolean] (24 + 42) == (0|1)")
		assert.Contains(err.Error(), "gnark.(*notBooleanTrace).Define")
		assert.Contains(err.Error(), "debug_test.go:")
	}
}

func getPlonkTrace(circuit, witness frontend.Circuit) (string, error) {
	ccs, err := frontend.Compile(ecc.BN254, backend.PLONK, circuit)
	if err != nil {
		return "", err
	}

	srs, err := test.NewKZGSRS(ccs)
	if err != nil {
		return "", err
	}
	pk, _, err := plonk.Setup(ccs, srs)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	_, err = plonk.Prove(ccs, pk, witness, backend.WithOutput(&buf))
	return buf.String(), err
}

func getGroth16Trace(circuit, witness frontend.Circuit) (string, error) {
	ccs, err := frontend.Compile(ecc.BN254, backend.GROTH16, circuit)
	if err != nil {
		return "", err
	}

	pk, err := groth16.DummySetup(ccs)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	_, err = groth16.Prove(ccs, pk, witness, backend.WithOutput(&buf))
	return buf.String(), err
}
