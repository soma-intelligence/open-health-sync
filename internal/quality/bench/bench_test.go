// SPDX-License-Identifier: AGPL-3.0-only

//go:build bench

package bench

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/quality"
)

// ModelVersion names the model and calibration this run writes.
const ModelVersion = "2026-10-24.1"

// maxTrain caps the figures of one metric and device a subject brings to a
// fit, so dense series and the synthetic people (weeks of data each) do not
// drown the rest.
const maxTrain = 600

func datasetInfo() map[string]*DatasetInfo {
	return map[string]*DatasetInfo{
		"walch": {Name: "Walch et al. 2019 (PhysioNet sleep-accel 1.0.0)", Licence: "ODC-By 1.0",
			Attribution: "Walch O, Huang Y, Forger D, Goldstein C. Sleep stage prediction with raw acceleration and photoplethysmography heart rate data derived from a consumer wearable device. Sleep 42(12), 2019. Data: PhysioNet, https://physionet.org/content/sleep-accel/1.0.0/, Open Data Commons Attribution License v1.0.",
			Tells:       "total sleep from wrist motion against polysomnography, one night per person; sleep detection",
			CannotTell:  "heart rate error (no ECG in the release), Apple's own sleep figures, more than one night per person"},
		"galaxyppg": {Name: "GalaxyPPG (Zenodo 14635823)", Licence: "CC BY 4.0",
			Attribution: "GalaxyPPG, Interactive Computing Lab, KAIST. Sci Data 12:892, 2025. Data: Zenodo record 14635823 (https://zenodo.org/records/14635823), CC BY 4.0.",
			Tells:       "Galaxy Watch 5 and Empatica E4 heart rate as each device computed it, against a Polar H10 chest strap, at rest, under stress, typing, walking, jogging and running; triple collocation without a reference",
			CannotTell:  "sleep, nightly figures, free living, other watch brands"},
		"ppg_dalia": {Name: "PPG-DaLiA (UCI 495)", Licence: "CC BY 4.0",
			Attribution: "Reiss A, Indlekofer I, Schmidt P, Van Laerhoven K. Deep PPG: large-scale heart rate estimation with convolutional neural networks. Sensors 19(14):3079, 2019. Data: UCI Machine Learning Repository, https://archive.ics.uci.edu/dataset/495/ppg+dalia, CC BY 4.0.",
			Tells:       "Empatica E4 heart rate (its own HR.csv) against chest ECG through daily life activities (sitting, stairs, table soccer, cycling, driving, lunch, walking, working)",
			CannotTell:  "consumer watch or ring outputs, sleep, nightly figures"},
		"synthetic": {Name: "Synthetic (internal/synth)", Licence: "in repository",
			Attribution: "Synthetic people of internal/synth, measured by simulated devices whose errors are drawn from the priors.",
			Tells:       "that the machinery is right when the priors are: coverage, shrinkage, triple collocation, copy detection",
			CannotTell:  "whether the priors are right; every Oura, WHOOP and Garmin figure in production rests on the priors alone, since no public dataset pairs their outputs with a reference"},
	}
}

func TestBench(t *testing.T) {
	dir := DataDir()
	info := datasetInfo()
	var recs []Record
	in := ReportInput{Version: ModelVersion, ContextConf: map[string]map[string]map[quality.Context]int{}}

	start := time.Now()
	if d, lag, err := LoadDalia(dir); err == nil {
		recs = append(recs, d...)
		in.DaliaLag = lag
		info["ppg_dalia"].Loaded = true
		t.Logf("ppg_dalia: %d figures, lag %.0f s", len(d), lag)
	} else {
		info["ppg_dalia"].Note = notRun(err)
		t.Logf("ppg_dalia: %v", err)
	}
	if g, gr, err := LoadGalaxy(dir); err == nil {
		recs = append(recs, g...)
		in.Galaxy = gr
		info["galaxyppg"].Loaded = true
		in.GalaxyTC = galaxyTC(gr)
		t.Logf("galaxyppg: %d figures, lags watch %.0f s, E4 %.0f s", len(g), gr.WatchLag, gr.E4Lag)
	} else {
		info["galaxyppg"].Note = notRun(err)
		t.Logf("galaxyppg: %v", err)
	}
	if w, wr, err := LoadWalch(dir); err == nil {
		recs = append(recs, w...)
		in.Walch = wr
		info["walch"].Loaded = true
		t.Logf("walch: %d nights", len(w))
	} else {
		info["walch"].Note = notRun(err)
		t.Logf("walch: %v", err)
	}
	s, sr := LoadSynthetic(12, 28)
	recs = append(recs, s...)
	in.Synth = sr
	info["synthetic"].Loaded = true
	t.Logf("synthetic: %d figures; loaded in %s", len(s), time.Since(start).Round(time.Second))

	for _, r := range recs {
		c := in.ContextConf[r.Dataset]
		if c == nil {
			c = map[string]map[quality.Context]int{}
			in.ContextConf[r.Dataset] = c
		}
		if c[r.Label] == nil {
			c[r.Label] = map[quality.Context]int{}
		}
		c[r.Label][r.Context]++
	}
	subj := map[string]map[string]bool{}
	for _, r := range recs {
		if subj[r.Dataset] == nil {
			subj[r.Dataset] = map[string]bool{}
		}
		subj[r.Dataset][r.Subject] = true
		info[r.Dataset].Figures++
	}
	for _, k := range []string{"walch", "galaxyppg", "ppg_dalia", "synthetic"} {
		info[k].Subjects = len(subj[k])
		in.Datasets = append(in.Datasets, *info[k])
	}

	start = time.Now()
	pri := quality.DefaultPriors()
	res := Backtest(recs, pri, maxTrain)
	in.Res = res
	t.Logf("backtest: %d figures in %s", len(recs), time.Since(start).Round(time.Second))
	for _, g := range Gates(res, Real) {
		t.Logf("gate (reference) %-50s %-24s %s", g.Name, g.Value, passFail(g.Passed))
	}
	for _, o := range GroupsOutside(res, Real) {
		t.Logf("outside: %s", o)
	}
	for _, g := range Gates(res, Synthetic) {
		t.Logf("gate (synthetic) %-50s %-24s %s", g.Name, g.Value, passFail(g.Passed))
	}
	md := Markdown(in)
	if os.Getenv("OHS_QUALITY_WRITE") != "1" {
		t.Log("set OHS_QUALITY_WRITE=1 to write the report and the model")
		t.Log("\n" + md)
		return
	}
	model, cal, err := FinalModel(res, pri, maxTrain, ModelVersion)
	if err != nil {
		t.Fatal(err)
	}
	model.Trained = trainedOn(in.Datasets)
	repo := filepath.Join("..", "..", "..")
	mb, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := cal.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "internal", "quality", "model.json"), append(mb, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "internal", "quality", "calibration.json"), append(cb, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "docs", "quality"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "docs", "quality", "report.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Log("wrote internal/quality/model.json, internal/quality/calibration.json, docs/quality/report.md")
}

func notRun(err error) string {
	if errors.Is(err, ErrMissing) {
		return "Not on disk when this report was made"
	}
	return "Not run: " + err.Error()
}

func trainedOn(ds []DatasetInfo) string {
	s := ""
	for _, d := range ds {
		if d.Loaded {
			if s != "" {
				s += ", "
			}
			s += d.Name
		}
	}
	return s
}

// galaxyTC runs triple collocation per subject on the Galaxy Watch, the E4
// and the Polar H10 aligned on 10 s buckets, and compares the two wrist
// estimates with the SD of their actual difference to the strap.
func galaxyTC(gr GalaxyResult) []TCCheck {
	var out []TCCheck
	for i, tr := range gr.Triples {
		x, y, z := quality.Align3(10*time.Second, tr[0], tr[1], tr[2])
		if len(x) < 100 {
			continue
		}
		tc, err := quality.TripleCollocation(x, y, z)
		if err != nil {
			continue
		}
		dx := make([]float64, len(x))
		dy := make([]float64, len(x))
		for k := range x {
			dx[k], dy[k] = x[k]-z[k], y[k]-z[k]
		}
		sub := "galaxyppg/" + string(rune('A'+i))
		out = append(out, TCCheck{sub, "galaxy_watch_5", math.Sqrt(tc.Var[0]), sdOf(dx), tc.Clipped[0]})
		out = append(out, TCCheck{sub, "empatica_e4", math.Sqrt(tc.Var[1]), sdOf(dy), tc.Clipped[1]})
		out = append(out, TCCheck{sub, "polar_h10 (the reference)", math.Sqrt(tc.Var[2]), 0, tc.Clipped[2]})
	}
	return out
}
