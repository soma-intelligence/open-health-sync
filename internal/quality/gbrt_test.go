// SPDX-License-Identifier: AGPL-3.0-only

package quality

import (
	"encoding/json"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGBRT_FitsAStep(t *testing.T) {
	var X [][]float64
	var y []float64
	for i := range 200 {
		x := float64(i)
		X = append(X, []float64{x})
		if x < 120 {
			y = append(y, 1)
		} else {
			y = append(y, 5)
		}
	}
	e, err := FitGBRT(X, y, []string{"x"}, GBRTParams{Trees: 50, LearningRate: 0.3, MaxDepth: 1, MinLeaf: 5, Bins: 256})
	require.NoError(t, err)
	assert.InDelta(t, 1, e.Predict([]float64{10}), 0.01)
	assert.InDelta(t, 5, e.Predict([]float64{150}), 0.01)
	assert.InDelta(t, 1, e.Predict([]float64{119}), 0.01)
}

// heteroscedastic draws log absolute errors whose scale depends on two of
// four features, the way wrist heart rate error depends on motion.
func heteroscedastic(seed uint64, n int) ([][]float64, []float64, []float64) {
	r := rand.New(rand.NewPCG(seed, 11))
	var X [][]float64
	var y, sd []float64
	for range n {
		motion, cat, noise1, noise2 := r.Float64(), float64(r.IntN(3)), r.Float64(), r.NormFloat64()
		s := 2 + 8*motion + 3*cat
		if r.Float64() < 0.1 {
			motion = math.NaN()
		}
		e := s * r.NormFloat64()
		X = append(X, []float64{motion, cat, noise1, noise2})
		y = append(y, math.Log(math.Abs(e)+0.5))
		sd = append(sd, s)
	}
	return X, y, sd
}

func TestGBRT_LearnsScaleAndIsDeterministic(t *testing.T) {
	X, y, _ := heteroscedastic(1, 6000)
	p := GBRTParams{Trees: 120, LearningRate: 0.1, MaxDepth: 3, MinLeaf: 40, Lambda: 1}
	names := []string{"motion", "cat", "noise1", "noise2"}
	e1, err := FitGBRT(X, y, names, p)
	require.NoError(t, err)
	e2, _ := FitGBRT(X, y, names, p)
	b1, _ := json.Marshal(e1)
	b2, _ := json.Marshal(e2)
	assert.Equal(t, string(b1), string(b2), "same data, same trees")

	// Held out: the prediction tracks log sigma. The log of a normal's
	// absolute value has variance pi^2/8 whatever the scale, so even the
	// true log sigma only gets about 15% below the mean's error.
	Xt, yt, sdt := heteroscedastic(2, 3000)
	var mseModel, mseBase float64
	mean := e1.Base
	for i := range Xt {
		mseModel += math.Pow(e1.Predict(Xt[i])-yt[i], 2)
		mseBase += math.Pow(mean-yt[i], 2)
	}
	assert.Less(t, mseModel, 0.9*mseBase)
	// Predictions rise with the true scale.
	var lo, hi []float64
	for i := range Xt {
		if sdt[i] < 5 {
			lo = append(lo, e1.Predict(Xt[i]))
		} else if sdt[i] > 11 {
			hi = append(hi, e1.Predict(Xt[i]))
		}
	}
	ml, _ := meanVar(lo)
	mh, _ := meanVar(hi)
	assert.Greater(t, mh-ml, 0.8)
}

func TestGBRT_JSONRoundTrip(t *testing.T) {
	X, y, _ := heteroscedastic(3, 2000)
	e, err := FitGBRT(X, y, []string{"a", "b", "c", "d"}, GBRTParams{Trees: 30, LearningRate: 0.2, MaxDepth: 3, MinLeaf: 20})
	require.NoError(t, err)
	b, err := json.Marshal(e)
	require.NoError(t, err)
	back, err := ParseEnsemble(b)
	require.NoError(t, err)
	for i := range 200 {
		assert.Equal(t, e.Predict(X[i]), back.Predict(X[i]))
	}
	assert.Equal(t, []string{"a", "b", "c", "d"}, back.Features)
}

func TestParseEnsemble_Rejects(t *testing.T) {
	for _, s := range []string{
		`{"features":["a"],"base":0,"learning_rate":0.1,"trees":[{"nodes":[{"f":0,"t":1,"l":5,"r":6}]}]}`,
		`{"features":["a"],"base":0,"learning_rate":0.1,"trees":[{"nodes":[{"f":3,"t":1,"l":1,"r":2},{"leaf":true},{"leaf":true}]}]}`,
		`{"features":["a"],"base":0,"learning_rate":0.1,"trees":[{"nodes":[]}]}`,
		`{"features":["a"],"base":0,"learning_rate":0.1,"trees":[{"nodes":[{"f":0,"t":1,"l":0,"r":0}]}]}`,
	} {
		_, err := ParseEnsemble([]byte(s))
		assert.Errorf(t, err, "accepted %s", s)
	}
}

func TestFitGBRT_Rejects(t *testing.T) {
	_, err := FitGBRT(nil, nil, nil, GBRTParams{})
	assert.Error(t, err)
	_, err = FitGBRT([][]float64{{1}, {2}}, []float64{1}, []string{"x"}, GBRTParams{Trees: 1})
	assert.Error(t, err)
	_, err = FitGBRT([][]float64{{1}, {2, 3}}, []float64{1, 2}, []string{"x"}, GBRTParams{Trees: 1})
	assert.Error(t, err)
}
