# SPDX-License-Identifier: AGPL-3.0-only
#
# One-off offline converter for the quality bench: reads PPG-DaLiA (UCI 495)
# straight from its zip and writes, per subject, the small CSV files the Go
# bench reads. Go has no pickle reader and the bench must not depend on
# numpy, so this script decodes the subject pickles itself.
#
# The pickles are untrusted data. They are read with a restricted unpickler
# that resolves only the handful of names a numpy array pickle uses, each to
# a stub defined here: nothing from the archive is imported or executed, and
# nothing is extracted to disk.
#
# Run with the system python in isolated mode, never from inside the data
# directory:
#
#   python3 -I scripts/quality/dalia_to_csv.py "$OHS_QUALITY_DATA/ppg-dalia/ppg_dalia.zip" "$OHS_QUALITY_DATA/ppg-dalia/csv"
#
# Output, per subject SX:
#   SX_windows.csv  t,hr_ref,activity,enmo: one row per 8 s window shifted by
#                   2 s (t is the window centre in seconds on the pickle's
#                   clock), the ECG reference heart rate, the activity id at
#                   the centre and the mean wrist ENMO (g) over the window
#   SX_e4hr.csv     t,hr: the heart rate the Empatica E4 computed itself
#                   (HR.csv), on the pickle's clock
#   SX_subject.csv  age,gender,height,weight,skin,sport
import array
import io
import math
import os
import pickle
import sys
import zipfile


class FakeDtype:
    def __init__(self, kind, *args):
        self.kind = kind
        self.order = "<"

    def __setstate__(self, state):
        if isinstance(state, tuple) and len(state) > 1 and isinstance(state[1], str):
            self.order = state[1]


class FakeArray:
    def __init__(self, *args):
        self.shape = ()
        self.dtype = None
        self.raw = b""

    def __setstate__(self, state):
        _version, shape, dtype, _fortran, raw = state
        self.shape, self.dtype = shape, dtype
        self.raw = raw.encode("latin1") if isinstance(raw, str) else bytes(raw)

    def values(self):
        codes = {"f8": "d", "f4": "f", "i8": "q", "i4": "i", "u1": "B", "i1": "b", "u8": "Q", "u4": "I", "i2": "h"}
        kind = self.dtype.kind if self.dtype else "f8"
        if kind not in codes:
            raise ValueError("unsupported dtype %r" % kind)
        a = array.array(codes[kind])
        a.frombytes(self.raw)
        if self.dtype and self.dtype.order == ">":
            a.byteswap()
        return a


def reconstruct(cls, shape, dtype):
    return FakeArray()


def scalar(dtype, raw):
    a = FakeArray()
    a.dtype = dtype
    a.raw = raw.encode("latin1") if isinstance(raw, str) else bytes(raw)
    a.shape = ()
    return a.values()[0]


ALLOWED = {
    ("numpy.core.multiarray", "_reconstruct"): reconstruct,
    ("numpy.core.multiarray", "scalar"): scalar,
    ("numpy", "ndarray"): FakeArray,
    ("numpy", "dtype"): FakeDtype,
    ("_codecs", "encode"): lambda s, enc="latin1": s.encode(enc),
}


class SafeUnpickler(pickle.Unpickler):
    def find_class(self, module, name):
        f = ALLOWED.get((module, name))
        if f is None:
            raise pickle.UnpicklingError("refusing %s.%s" % (module, name))
        return f


def rows(fake):
    """Returns a list of rows of a 2-D array, or a flat list."""
    v = fake.values()
    if len(fake.shape) == 2:
        n, m = fake.shape
        return [v[i * m:(i + 1) * m] for i in range(n)]
    return list(v)


def e4_series(e4zip, name):
    with e4zip.open(name) as f:
        text = io.TextIOWrapper(f, encoding="utf-8").read().split("\n")
    start = float(text[0].split(",")[0])
    rate = float(text[1].split(",")[0])
    vals = [line for line in text[2:] if line.strip()]
    return start, rate, vals


def find_offset(csv_bvp, pkl_bvp, n=320):
    """Index of the CSV sample that is the pickle's first BVP sample."""
    head = pkl_bvp[:n]
    first = head[0]
    for k in range(len(csv_bvp) - n):
        if csv_bvp[k] == first and csv_bvp[k:k + n] == head:
            return k, True
    # Not exact: best mean absolute match over the first minute.
    best, bestk = float("inf"), 0
    for k in range(0, min(len(csv_bvp) - n, 64 * 600)):
        d = sum(abs(csv_bvp[k + i] - head[i]) for i in range(0, n, 4))
        if d < best:
            best, bestk = d, k
    return bestk, False


def convert(outer_path, out_dir):
    os.makedirs(out_dir, exist_ok=True)
    outer = zipfile.ZipFile(outer_path)
    with outer.open("data.zip") as f:
        inner = zipfile.ZipFile(f)
        names = inner.namelist()
        subjects = sorted({n.split("/")[1] for n in names if n.count("/") >= 2 and n.split("/")[1].startswith("S")},
                          key=lambda s: int(s[1:]))
        for s in subjects:
            base = "PPG_FieldStudy/%s/%s" % (s, s)
            with inner.open(base + ".pkl") as pf:
                data = SafeUnpickler(pf, encoding="latin1").load()
            label = list(data["label"].values())
            act = list(data["activity"].values())
            wrist = data["signal"]["wrist"]
            acc = rows(wrist["ACC"])
            bvp = list(wrist["BVP"].values())
            mags = sorted(math.sqrt(a[0] ** 2 + a[1] ** 2 + a[2] ** 2) for a in acc[:: max(1, len(acc) // 2000)])
            unit = 64.0 if mags[len(mags) // 2] > 8 else 1.0
            enmo = [max(0.0, math.sqrt(a[0] ** 2 + a[1] ** 2 + a[2] ** 2) / unit - 1.0) for a in acc]
            with open(os.path.join(out_dir, s + "_windows.csv"), "w") as w:
                w.write("t,hr_ref,activity,enmo\n")
                for j, hr in enumerate(label):
                    lo, hi = j * 2 * 32, (j * 2 + 8) * 32
                    seg = enmo[lo:hi]
                    m = sum(seg) / len(seg) if seg else float("nan")
                    c = int((j * 2 + 4) * 4)
                    a = int(act[c]) if c < len(act) else -1
                    w.write("%.1f,%.4f,%d,%.5f\n" % (j * 2 + 4, hr, a, m))
            e4 = zipfile.ZipFile(io.BytesIO(inner.read(base + "_E4.zip")))
            bstart, brate, bvals = e4_series(e4, "BVP.csv")
            csv_bvp = [float(v) for v in bvals]
            k, exact = find_offset(csv_bvp, bvp)
            pkl0 = bstart + k / brate
            hstart, hrate, hvals = e4_series(e4, "HR.csv")
            with open(os.path.join(out_dir, s + "_e4hr.csv"), "w") as w:
                w.write("t,hr\n")
                for i, v in enumerate(hvals):
                    w.write("%.3f,%s\n" % (hstart + i / hrate - pkl0, v.strip()))
            q = data.get("questionnaire", {})
            with open(os.path.join(out_dir, s + "_subject.csv"), "w") as w:
                w.write("age,gender,height,weight,skin,sport\n")
                w.write("%s,%s,%s,%s,%s,%s\n" % tuple(str(q.get(k, "")).strip() for k in ("AGE", "Gender", "HEIGHT", "WEIGHT", "SKIN", "SPORT")))
            print("%s: %d windows, %d E4 HR, BVP offset %d (%s), ACC unit 1/%g g" % (s, len(label), len(hvals), k, "exact" if exact else "approximate", unit), flush=True)
            del data, acc, bvp, enmo, csv_bvp


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit("usage: python3 -I dalia_to_csv.py ppg_dalia.zip out_dir")
    convert(sys.argv[1], sys.argv[2])
