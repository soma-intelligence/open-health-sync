// SPDX-License-Identifier: AGPL-3.0-only

// Package activityfiles renders a synthetic timeline as activity files, FIT,
// GPX and TCX, wrapped in the upload lines of POST /v1/ingest/activity-files.
//
// Sources: the FIT Protocol and the FIT Profile of the FIT SDK 21.218
// (developer.garmin.com/fit), the GPX 1.1 schema (topografix.com/GPX/1/1),
// the Training Center Database v2 schema and Garmin's TrackPointExtension v2
// schema. The line envelope ({"type":"fit","data":"<base64>"}) is the upload
// contract of docs/connectors/activity-files.md.
//
// The document scenarios do not apply to a base64 field, so the file
// scenarios are written here: valid variants (big endian messages, the
// legacy 12 byte header, invalid field values, an unknown message, offsets
// in GPX times) and faulty files (truncated, a wrong CRC, data before its
// definition, broken XML, a route without times, timestamps at the FIT
// epoch, heart rates no body reaches).
package activityfiles

import (
	"encoding/base64"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/synth"
)

// Slug is the connector the lines are for.
const Slug = "activity_files"

// fitSport maps the generator's sports to the FIT sport and sub_sport enums.
var fitSport = map[synth.Sport][2]int{
	synth.SportRun: {1, 0}, synth.SportWalk: {11, 0}, synth.SportCycle: {2, 0}, synth.SportSwim: {5, 17},
	synth.SportStrength: {10, 20}, synth.SportYoga: {10, 0}, synth.SportHIIT: {62, 0}, synth.SportRow: {15, 14},
	synth.SportHike: {17, 0}, synth.SportOther: {0, 0},
}

// tcxSport is the Sport_t enumeration: Running, Biking or Other.
func tcxSport(s synth.Sport) string {
	switch s {
	case synth.SportRun, synth.SportWalk, synth.SportHike:
		return "Running"
	case synth.SportCycle:
		return "Biking"
	}
	return "Other"
}

// line wraps a file as the upload contract does.
func line(format string, data []byte) []byte {
	return []byte(`{"type":"` + format + `","data":"` + base64.StdEncoding.EncodeToString(data) + `"}`)
}

// fitOptions are the valid variants and the faults of a FIT file.
type fitOptions struct {
	big, header12, unknownMsg bool
	maker                     int
	epoch                     bool // every timestamp at the FIT epoch
	hr                        int  // a heart rate to write in place of the measured one, 0 for none
	virtual                   bool
}

// point is one second of a recording.
type point struct {
	t                time.Time
	lat, lon, ele, v float64
	dist             float64
	hr, cad          float64
	power            float64
	paused, hasRoute bool
}

func track(tl *synth.Timeline, w synth.Workout, step time.Duration) []point {
	var pts []point
	route := w.Sport.Outdoor() && !w.Indoor
	dist := 0.0
	for t := w.Start; t.Before(w.End); t = t.Add(step) {
		p := point{t: t, hr: tl.HeartRate(t), cad: w.AvgCadence, power: w.AvgPowerW, paused: w.Paused(t), hasRoute: route}
		if route {
			p.lat, p.lon, p.ele, p.v = tl.Route(w, t)
		} else if w.Moving() > 0 {
			p.v = w.DistanceM / w.Moving().Seconds()
		}
		if !p.paused {
			dist += p.v * step.Seconds()
		}
		p.dist = dist
		pts = append(pts, p)
	}
	return pts
}

func fitFile(w synth.Workout, pts []point, o fitOptions) []byte {
	f := newFIT(o.big)
	ts := func(t time.Time) uint32 {
		if o.epoch {
			return 0
		}
		return fitTime(t)
	}
	sp := fitSport[w.Sport]
	if o.virtual {
		sp = [2]int{2, 58}
	}
	f.msg(0, mesgFileID, []field{{0, btEnum, 4}, {1, btUint16, o.maker}, {2, btUint16, 4315}, {3, btUint32z, 3412345678}, {4, btUint32, ts(w.Start)}})
	if o.unknownMsg {
		// A manufacturer specific message (0xFF00 and up): decoders skip it.
		f.msg(3, 0xFF10, []field{{0, btUint32, 12345}, {1, btUint8, 7}})
	}
	f.msg(1, mesgEvent, []field{{253, btUint32, ts(w.Start)}, {0, btEnum, 0}, {1, btEnum, 0}})
	paused := false
	for i, p := range pts {
		if p.paused != paused {
			kind := 0 // start
			if p.paused {
				kind = 4 // stop_all
			}
			f.msg(1, mesgEvent, []field{{253, btUint32, ts(p.t)}, {0, btEnum, 0}, {1, btEnum, kind}})
			paused = p.paused
		}
		if p.paused {
			continue
		}
		var hr any = int(math.Round(p.hr))
		if i%97 == 13 {
			// A second the strap lost contact: the invalid value.
			hr = nil
		}
		if o.hr != 0 && i == len(pts)/2 {
			hr = o.hr
		}
		fs := []field{{253, btUint32, ts(p.t)}, {3, btUint8, hr}, {5, btUint32, p.dist * 100}, {6, btUint16, p.v * 1000}}
		if p.hasRoute && !o.virtual {
			fs = append(fs, field{0, btSint32, semicircles(p.lat)}, field{1, btSint32, semicircles(p.lon)}, field{2, btUint16, (p.ele + 500) * 5})
		}
		if p.cad > 0 {
			fs = append(fs, field{4, btUint8, p.cad / 2})
		}
		if p.power > 0 {
			fs = append(fs, field{7, btUint16, p.power})
		}
		f.msg(2, mesgRecord, fs)
	}
	f.msg(1, mesgEvent, []field{{253, btUint32, ts(w.End)}, {0, btEnum, 0}, {1, btEnum, 4}})
	elapsed := w.End.Sub(w.Start).Seconds() * 1000
	timer := w.Moving().Seconds() * 1000
	f.msg(4, mesgLap, []field{{254, btUint16, 0}, {253, btUint32, ts(w.End)}, {0, btEnum, 9}, {1, btEnum, 1}, {2, btUint32, ts(w.Start)},
		{7, btUint32, elapsed}, {8, btUint32, timer}, {9, btUint32, w.DistanceM * 100}, {11, btUint16, w.EnergyKcal}, {15, btUint8, w.AvgHR}})
	ss := []field{{254, btUint16, 0}, {253, btUint32, ts(w.End)}, {0, btEnum, 8}, {1, btEnum, 1}, {2, btUint32, ts(w.Start)},
		{5, btEnum, sp[0]}, {6, btEnum, sp[1]}, {7, btUint32, elapsed}, {8, btUint32, timer}, {9, btUint32, w.DistanceM * 100},
		{11, btUint16, w.EnergyKcal}, {16, btUint8, w.AvgHR}, {17, btUint8, w.MaxHR}, {22, btUint16, w.AscentM}, {25, btUint16, 0}, {26, btUint16, 1}}
	if len(pts) > 0 && pts[0].hasRoute && !o.virtual {
		ss = append(ss, field{3, btSint32, semicircles(pts[0].lat)}, field{4, btSint32, semicircles(pts[0].lon)})
	}
	if w.AvgPowerW > 0 {
		ss = append(ss, field{20, btUint16, w.AvgPowerW})
	}
	f.msg(5, mesgSession, ss)
	f.msg(6, mesgActivity, []field{{253, btUint32, ts(w.End)}, {0, btUint32, timer}, {1, btUint16, 1}, {2, btEnum, 0}, {3, btEnum, 26}, {4, btEnum, 1}})
	return f.bytes(o.header12)
}

func gpxFile(tl *synth.Timeline, w synth.Workout, pts []point, withTimes, localTimes bool) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<gpx version="1.1" creator="StravaGPX" xmlns="http://www.topografix.com/GPX/1/1" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:gpxtpx="http://www.garmin.com/xmlschemas/TrackPointExtension/v2" xsi:schemaLocation="http://www.topografix.com/GPX/1/1 http://www.topografix.com/GPX/1/1/gpx.xsd">` + "\n")
	tm := func(t time.Time) string {
		if localTimes {
			return tl.Local(t).Format(time.RFC3339)
		}
		return t.UTC().Format(time.RFC3339)
	}
	if withTimes {
		fmt.Fprintf(&b, "<metadata><time>%s</time></metadata>\n", tm(w.Start))
	}
	fmt.Fprintf(&b, "<trk><name>%s &amp; friends</name><type>%s</type><trkseg>\n", strings.ToUpper(string(w.Sport[:1]))+string(w.Sport[1:]), w.Sport)
	for _, p := range pts {
		if p.paused || !p.hasRoute {
			continue
		}
		fmt.Fprintf(&b, `<trkpt lat="%.7f" lon="%.7f"><ele>%.1f</ele>`, p.lat, p.lon, p.ele)
		if withTimes {
			fmt.Fprintf(&b, "<time>%s</time>", tm(p.t))
		}
		fmt.Fprintf(&b, "<extensions><gpxtpx:TrackPointExtension><gpxtpx:hr>%d</gpxtpx:hr>", int(math.Round(p.hr)))
		if p.cad > 0 {
			fmt.Fprintf(&b, "<gpxtpx:cad>%d</gpxtpx:cad>", int(p.cad/2))
		}
		b.WriteString("</gpxtpx:TrackPointExtension></extensions></trkpt>\n")
	}
	b.WriteString("</trkseg></trk>\n</gpx>\n")
	return []byte(b.String())
}

func tcxFile(w synth.Workout, pts []point, sport string) []byte {
	var b strings.Builder
	iso := func(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<TrainingCenterDatabase xmlns="http://www.garmin.com/xmlschemas/TrainingCenterDatabase/v2" xmlns:ns3="http://www.garmin.com/xmlschemas/ActivityExtension/v2">` + "\n")
	fmt.Fprintf(&b, "<Activities><Activity Sport=%q><Id>%s</Id>\n", sport, iso(w.Start))
	fmt.Fprintf(&b, "<Lap StartTime=%q><TotalTimeSeconds>%.1f</TotalTimeSeconds><DistanceMeters>%.1f</DistanceMeters><Calories>%d</Calories>",
		iso(w.Start), w.Moving().Seconds(), w.DistanceM, int(w.EnergyKcal))
	fmt.Fprintf(&b, "<AverageHeartRateBpm><Value>%d</Value></AverageHeartRateBpm><MaximumHeartRateBpm><Value>%d</Value></MaximumHeartRateBpm>", int(w.AvgHR), int(w.MaxHR))
	b.WriteString("<Intensity>Active</Intensity><TriggerMethod>Manual</TriggerMethod><Track>\n")
	for _, p := range pts {
		if p.paused {
			continue
		}
		fmt.Fprintf(&b, "<Trackpoint><Time>%s</Time>", iso(p.t))
		if p.hasRoute {
			fmt.Fprintf(&b, "<Position><LatitudeDegrees>%.7f</LatitudeDegrees><LongitudeDegrees>%.7f</LongitudeDegrees></Position><AltitudeMeters>%.1f</AltitudeMeters>", p.lat, p.lon, p.ele)
		}
		fmt.Fprintf(&b, "<DistanceMeters>%.1f</DistanceMeters><HeartRateBpm><Value>%d</Value></HeartRateBpm>", p.dist, int(math.Round(p.hr)))
		if p.cad > 0 && sport == "Biking" {
			fmt.Fprintf(&b, "<Cadence>%d</Cadence>", int(p.cad))
		}
		if p.power > 0 {
			fmt.Fprintf(&b, "<Extensions><ns3:TPX><ns3:Watts>%d</ns3:Watts></ns3:TPX></Extensions>", int(p.power))
		}
		b.WriteString("</Trackpoint>\n")
	}
	b.WriteString("</Track></Lap><Creator><Name>Polar Vantage V3</Name></Creator></Activity></Activities>\n</TrainingCenterDatabase>\n")
	return []byte(b.String())
}

// Lines renders tl as activity file lines, one file per line.
func Lines(tl *synth.Timeline) []synth.Line {
	e := synth.NewEmitter(tl, Slug)
	o := tl.Opt
	makers := []int{1, 294, 32, 23} // garmin, coros, wahoo_fitness, suunto
	add := func(format string, w synth.Workout, data []byte, f synth.Fault, tag string) {
		e.Raw(synth.Line{DataType: format, Day: w.Start.UTC().Truncate(24 * time.Hour), JSON: line(format, data), Fault: f, Tag: tag})
	}
	routeDone := false
	for i, w := range tl.Recorded() {
		if w.End.After(o.Now) {
			continue
		}
		pts := track(tl, w, time.Second)
		fo := fitOptions{maker: makers[i%len(makers)], big: i%5 == 3, header12: i%7 == 4,
			virtual: w.Sport == synth.SportCycle && w.Indoor}
		if fo.virtual {
			fo.maker = 260 // zwift
		}
		fit := fitFile(w, pts, fo)
		tag := ""
		switch {
		case fo.big:
			tag = "big_endian"
		case fo.header12:
			tag = "header12"
		}
		add("fit", w, fit, synth.FaultNone, tag)
		if i == 0 || o.Has(synth.MultiDevice) {
			// The same workout exported from Strava (GPX) and Polar Flow
			// (TCX): three files of one session.
			slow := track(tl, w, 2*time.Second)
			if w.Sport.Outdoor() && !w.Indoor {
				add("gpx", w, gpxFile(tl, w, slow, true, i%2 == 1), synth.FaultNone, map[bool]string{true: "local_times"}[i%2 == 1])
			}
			add("tcx", w, tcxFile(w, slow, tcxSport(w.Sport)), synth.FaultNone, "")
		}
		if outdoor := w.Sport.Outdoor() && !w.Indoor; outdoor && !routeDone {
			routeDone = true
			if o.Has(synth.Missing) {
				add("gpx", w, gpxFile(tl, w, pts, false, false), synth.FaultMissingField, "route_without_times")
			}
			if o.Has(synth.Malformed) {
				g := gpxFile(tl, w, pts, true, false)
				add("gpx", w, g[:len(g)*2/3], synth.FaultMalformed, "truncated_xml")
			}
		}
		if i != 0 {
			continue
		}
		// The file scenarios, on the first workout.
		if o.Has(synth.Duplicates) {
			add("fit", w, fit, synth.FaultNone, "duplicate")
		}
		if o.Has(synth.Unknown) {
			u := fo
			u.unknownMsg = true
			add("fit", w, fitFile(w, pts, u), synth.FaultNone, "unknown_message")
			add("fit", w, fitFile(w, pts, fitOptions{maker: 255}), synth.FaultNone, "maker:development")
		}
		if o.Has(synth.Huge) {
			long := w
			long.End = w.Start.Add(20 * time.Hour)
			add("fit", w, fitFile(long, track(tl, long, time.Second), fo), synth.FaultNone, "twenty_hours")
		}
		if o.Has(synth.Timestamps) {
			ep := fo
			ep.epoch = true
			add("fit", w, fitFile(w, pts, ep), synth.FaultTimestamp, "fit_epoch")
		}
		if o.Has(synth.OutOfRange) {
			hr := fo
			hr.hr = 254
			add("fit", w, fitFile(w, pts, hr), synth.FaultOutOfRange, "heart_rate=254")
		}
		if o.Has(synth.Malformed) {
			add("fit", w, fit[:len(fit)/2], synth.FaultMalformed, "truncated")
			bad := append([]byte(nil), fit...)
			bad[len(bad)-1] ^= 0xFF
			add("fit", w, bad, synth.FaultMalformed, "bad_crc")
			add("fit", w, orphanData(), synth.FaultMalformed, "data_before_definition")
			add("fit", w, []byte("not a fit file"), synth.FaultMalformed, "garbage")
			add("tcx", w, []byte(`<TrainingCenterDatabase xmlns="http://www.garmin.com/xmlschemas/TrainingCenterDatabase/v2"><Activities><Activity Sport="Running"><Id>`), synth.FaultMalformed, "truncated_xml")
			e.Raw(synth.Line{DataType: "fit", Day: w.Start.UTC().Truncate(24 * time.Hour), JSON: []byte(`{"type":"fit","data":"%%% not base64"}`), Fault: synth.FaultMalformed, Tag: "bad_base64"})
		}
	}
	return e.Lines()
}

// orphanData is a FIT file whose first record is a data message no
// definition announced.
func orphanData() []byte {
	f := newFIT(false)
	f.data.Write([]byte{0x00, 4, 0, 1, 0})
	return f.bytes(false)
}
