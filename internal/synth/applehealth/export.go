// SPDX-License-Identifier: AGPL-3.0-only

package applehealth

import (
	"archive/zip"
	"bufio"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/synth"
)

// ExportFault is a defect an export archive can carry on purpose.
type ExportFault string

// Archive level defects, for the Malformed scenario. Each one gives an
// archive the importer must survive: refuse it with an error, or import what
// it can.
const (
	ExportTruncatedXML  ExportFault = "truncated_xml"   // export.xml cut in the middle of an element
	ExportMissingRoute  ExportFault = "missing_route"   // a FileReference to a route file the archive lacks
	ExportBrokenRoute   ExportFault = "broken_route"    // a route file that is not GPX
	ExportBrokenECG     ExportFault = "broken_ecg"      // an ECG file with a bad header and text in the samples
	ExportBadDates      ExportFault = "bad_dates"       // records with unreadable dates and end before start
	ExportUnknownTypes  ExportFault = "unknown_types"   // elements and record types of a later HealthKit
	ExportMacOSMetadata ExportFault = "macos_metadata"  // __MACOSX entries, as Finder's compress adds
	ExportNoXML         ExportFault = "no_export_xml"   // an archive without export.xml
	ExportCRLF          ExportFault = "crlf"            // ECG files with Windows line ends
	ExportPerInstantTZ  ExportFault = "per_instant_tz"  // dates with each instant's own offset instead of the export's
	ExportBadValues     ExportFault = "bad_values"      // values that are not numbers, NaN, empty units
	ExportDuplicateFile ExportFault = "duplicate_entry" // the same route file twice in the zip
)

// ExportFaults lists every archive level defect.
var ExportFaults = []ExportFault{ExportTruncatedXML, ExportMissingRoute, ExportBrokenRoute, ExportBrokenECG, ExportBadDates,
	ExportUnknownTypes, ExportMacOSMetadata, ExportNoXML, ExportCRLF, ExportPerInstantTZ, ExportBadValues, ExportDuplicateFile}

// ExportOptions shape an archive.
type ExportOptions struct {
	// Units picks imperial or other units; metric when nil.
	Units func(q quantity) altUnit
	// Faults are the archive defects to put in.
	Faults []ExportFault
	// Locale of HealthData, en_US when empty.
	Locale string
}

func (o ExportOptions) has(f ExportFault) bool {
	for _, x := range o.Faults {
		if x == f {
			return true
		}
	}
	return false
}

// ExportStats counts what an archive holds.
type ExportStats struct {
	Records, Correlations, Workouts, Summaries, Routes, ECGs int
	ByType                                                   map[string]int
}

// ImperialUnits picks the first alternative unit of every type that has one:
// pounds, miles, Fahrenheit, mmol/L, fluid ounces.
func ImperialUnits(q quantity) altUnit {
	if len(q.Alt) > 0 {
		return q.Alt[0]
	}
	return altUnit{}
}

// WriteExport writes the export archive of tl to w.
func WriteExport(w io.Writer, tl *synth.Timeline, o ExportOptions) (ExportStats, error) {
	zw := zip.NewWriter(w)
	g := NewGenerator(tl)
	g.Units = o.Units
	if tl.Opt.Has(synth.UnitVariants) && g.Units == nil {
		g.Units = ImperialUnits
	}
	ex := &exporter{g: g, o: o, stats: ExportStats{ByType: map[string]int{}}}
	ex.fixed = tl.Opt.Now.In(tl.Home)
	_, off := ex.fixed.Zone()
	ex.loc = time.FixedZone("", off)

	if !o.has(ExportNoXML) {
		f, err := create(zw, "apple_health_export/export.xml", tl.Opt.Now)
		if err != nil {
			return ex.stats, err
		}
		if err := ex.xml(f); err != nil {
			return ex.stats, err
		}
	}
	if err := ex.routes(zw); err != nil {
		return ex.stats, err
	}
	if err := ex.ecgs(zw); err != nil {
		return ex.stats, err
	}
	if err := ex.extras(zw); err != nil {
		return ex.stats, err
	}
	return ex.stats, zw.Close()
}

func create(zw *zip.Writer, name string, mod time.Time) (io.Writer, error) {
	return zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: mod.UTC()})
}

type exporter struct {
	g     *Generator
	o     ExportOptions
	stats ExportStats
	fixed time.Time
	loc   *time.Location
	// routes and their workouts, collected while writing the XML.
	routeRefs []routeRef
	bw        *bufio.Writer
}

type routeRef struct {
	Path string
	W    synth.Workout
}

// date prints a time the way the export does: in the zone the phone is in
// when exporting, with that moment's offset for every date, whatever the
// offset was when the sample was taken.
func (ex *exporter) date(t time.Time) string {
	if ex.o.has(ExportPerInstantTZ) {
		return t.In(ex.g.TL.ZoneAt(t)).Format("2006-01-02 15:04:05 -0700")
	}
	return t.In(ex.loc).Format("2006-01-02 15:04:05 -0700")
}

func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func attr(b *strings.Builder, k, v string) {
	b.WriteByte(' ')
	b.WriteString(k)
	b.WriteString(`="`)
	_ = xml.EscapeText(b, []byte(v))
	b.WriteByte('"')
}

func (ex *exporter) device(s Source) string {
	if s.DeviceName == "" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<<HKDevice: 0x%x>, name:%s", 0x300000000+len(s.Name)*4096+len(s.Model), s.DeviceName)
	if s.Manufacturer != "" {
		b.WriteString(", manufacturer:" + s.Manufacturer)
	}
	if s.Model != "" {
		b.WriteString(", model:" + s.Model)
	}
	if s.Hardware != "" {
		b.WriteString(", hardware:" + s.Hardware)
	}
	if s.Software != "" {
		b.WriteString(", software:" + s.Software)
	}
	b.WriteString(">")
	return b.String()
}

func (ex *exporter) xml(w io.Writer) error {
	tl := ex.g.TL
	ex.bw = bufio.NewWriterSize(w, 1<<20)
	bw := ex.bw
	locale := ex.o.Locale
	if locale == "" {
		locale = "en_US"
	}
	_, _ = bw.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	_, _ = bw.WriteString(dtd)
	fmt.Fprintf(bw, "<HealthData locale=%q>\n", locale)
	fmt.Fprintf(bw, " <ExportDate value=%q/>\n", ex.date(tl.Opt.Now))
	sex := "HKBiologicalSexMale"
	if tl.P.Sex == "female" {
		sex = "HKBiologicalSexFemale"
	}
	fmt.Fprintf(bw, " <Me HKCharacteristicTypeIdentifierDateOfBirth=%q HKCharacteristicTypeIdentifierBiologicalSex=%q HKCharacteristicTypeIdentifierBloodType=\"HKBloodTypeNotSet\" HKCharacteristicTypeIdentifierFitzpatrickSkinType=\"HKFitzpatrickSkinTypeNotSet\" HKCharacteristicTypeIdentifierCardioFitnessMedicationsUse=\"None\"/>\n",
		tl.P.BirthDate, sex)

	total := 0
	for _, typ := range Types() {
		var err error
		ex.g.Each(typ, func(s Sample) {
			if err != nil {
				return
			}
			err = ex.sample(s, 1)
			total++
		})
		if err != nil {
			return err
		}
		if ex.o.has(ExportTruncatedXML) && total > 200 {
			// Cut inside an element, as a copy interrupted mid-write leaves it.
			_, _ = bw.WriteString(` <Record type="HKQuantityTypeIdentifierHeartRate" sourceName="`)
			return bw.Flush()
		}
	}
	if ex.o.has(ExportUnknownTypes) {
		_, _ = bw.WriteString(" <Record type=\"HKQuantityTypeIdentifierFutureMetric\" sourceName=\"Health\" unit=\"zz\" value=\"1\" startDate=\"" + ex.date(tl.Days[0].Start.Add(9*time.Hour)) + "\" endDate=\"" + ex.date(tl.Days[0].Start.Add(9*time.Hour)) + "\"/>\n")
		_, _ = bw.WriteString(" <FutureElement kind=\"something\"><Child value=\"1\"/></FutureElement>\n")
		_, _ = bw.WriteString(" <Record type=\"HKCategoryTypeIdentifierFutureEvent\" sourceName=\"Health\" value=\"HKCategoryValueFutureThing\" startDate=\"" + ex.date(tl.Days[0].Start.Add(9*time.Hour)) + "\" endDate=\"" + ex.date(tl.Days[0].Start.Add(10*time.Hour)) + "\"/>\n")
	}
	if ex.o.has(ExportBadDates) {
		s := tl.Days[0].Start.Add(10 * time.Hour)
		fmt.Fprintf(bw, " <Record type=\"HKQuantityTypeIdentifierStepCount\" sourceName=\"Health\" unit=\"count\" value=\"10\" startDate=\"yesterday\" endDate=%q/>\n", ex.date(s))
		fmt.Fprintf(bw, " <Record type=\"HKQuantityTypeIdentifierStepCount\" sourceName=\"Health\" unit=\"count\" value=\"10\" startDate=%q endDate=%q/>\n", ex.date(s.Add(time.Hour)), ex.date(s))
		fmt.Fprintf(bw, " <Record type=\"HKQuantityTypeIdentifierHeartRate\" sourceName=\"Health\" unit=\"count/min\" value=\"60\" startDate=\"2026-02-30 25:61:00 +0100\" endDate=\"2026-02-30 25:61:00 +0100\"/>\n")
		fmt.Fprintf(bw, " <Record type=\"HKQuantityTypeIdentifierHeartRate\" sourceName=\"Health\" unit=\"count/min\" value=\"60\" startDate=\"1970-01-01 00:00:00 +0000\" endDate=\"1970-01-01 00:00:00 +0000\"/>\n")
	}
	if ex.o.has(ExportBadValues) {
		s := ex.date(tl.Days[0].Start.Add(11 * time.Hour))
		for _, v := range []string{"", "NaN", "inf", "1,5", "72 bpm", "-0", "1e400"} {
			fmt.Fprintf(bw, " <Record type=\"HKQuantityTypeIdentifierHeartRate\" sourceName=\"Health\" unit=\"count/min\" value=%q startDate=%q endDate=%q/>\n", v, s, s)
		}
		fmt.Fprintf(bw, " <Record type=\"HKQuantityTypeIdentifierBodyMass\" sourceName=\"Health\" unit=\"\" value=\"70\" startDate=%q endDate=%q/>\n", s, s)
	}
	for _, d := range tl.Days {
		if d.Missing {
			continue
		}
		ex.summary(d)
	}
	ex.audiogram()
	ex.clinical()
	_, _ = bw.WriteString("</HealthData>\n")
	return bw.Flush()
}

func (ex *exporter) common(b *strings.Builder, s Sample) {
	attr(b, "sourceName", s.Src.Name)
	if s.Src.Version != "" {
		attr(b, "sourceVersion", s.Src.Version)
	}
	if d := ex.device(s.Src); d != "" {
		attr(b, "device", d)
	}
}

func (ex *exporter) meta(b *strings.Builder, indent string, s Sample) {
	var md []MetaEntry
	if s.UserEntered {
		md = append(md, MetaEntry{"HKWasUserEntered", "1"})
	}
	if s.TZ != "" {
		md = append(md, MetaEntry{"HKTimeZone", s.TZ})
	}
	md = append(md, s.Meta...)
	for _, m := range md {
		b.WriteString(indent + "<MetadataEntry")
		attr(b, "key", m.Key)
		attr(b, "value", m.Value)
		b.WriteString("/>\n")
	}
}

// sample writes one Record, Correlation or Workout.
func (ex *exporter) sample(s Sample, depth int) error {
	ind := strings.Repeat(" ", depth)
	var b strings.Builder
	switch s.Kind {
	case KindQuantity, KindCategory:
		b.WriteString(ind + "<Record")
		attr(&b, "type", s.Type)
		ex.common(&b, s)
		if s.Kind == KindQuantity {
			attr(&b, "unit", s.Unit)
		}
		attr(&b, "creationDate", ex.date(s.Created))
		attr(&b, "startDate", ex.date(s.Start))
		attr(&b, "endDate", ex.date(s.End))
		if s.Kind == KindQuantity {
			attr(&b, "value", num(s.Value))
		} else {
			attr(&b, "value", s.CatName)
		}
		if !s.UserEntered && s.TZ == "" && len(s.Meta) == 0 && len(s.Beats) == 0 {
			b.WriteString("/>\n")
			break
		}
		b.WriteString(">\n")
		ex.meta(&b, ind+" ", s)
		if len(s.Beats) > 0 {
			b.WriteString(ind + " <HeartRateVariabilityMetadataList>\n")
			for _, bt := range s.Beats {
				lt := bt.At.In(ex.loc)
				fmt.Fprintf(&b, "%s  <InstantaneousBeatsPerMinute bpm=\"%s\" time=\"%s.%02d\"/>\n", ind, num(bt.BPM), lt.Format("15:04:05"), lt.Nanosecond()/1e7)
			}
			b.WriteString(ind + " </HeartRateVariabilityMetadataList>\n")
		}
		b.WriteString(ind + "</Record>\n")
		if depth == 1 {
			ex.stats.Records++
			ex.stats.ByType[s.Type]++
		}
	case KindCorrelation:
		b.WriteString(ind + "<Correlation")
		attr(&b, "type", s.Type)
		ex.common(&b, s)
		attr(&b, "creationDate", ex.date(s.Created))
		attr(&b, "startDate", ex.date(s.Start))
		attr(&b, "endDate", ex.date(s.End))
		b.WriteString(">\n")
		ex.meta(&b, ind+" ", s)
		_, _ = ex.bw.WriteString(b.String())
		b.Reset()
		for _, c := range s.Children {
			if err := ex.sample(c, depth+1); err != nil {
				return err
			}
		}
		b.WriteString(ind + "</Correlation>\n")
		ex.stats.Correlations++
		ex.stats.ByType[s.Type]++
	case KindWorkout:
		ex.workout(&b, s, ind)
		ex.stats.Workouts++
		ex.stats.ByType[s.Workout.Activity]++
	}
	_, err := ex.bw.WriteString(b.String())
	return err
}

func (ex *exporter) workout(b *strings.Builder, s Sample, ind string) {
	wd := s.Workout
	b.WriteString(ind + "<Workout")
	attr(b, "workoutActivityType", wd.Activity)
	attr(b, "duration", num(synth.Round(wd.Duration.Minutes(), 10)))
	attr(b, "durationUnit", "min")
	if wd.OldStyle {
		if wd.TotalDistanceKM > 0 {
			attr(b, "totalDistance", num(wd.TotalDistanceKM))
			attr(b, "totalDistanceUnit", "km")
		}
		if wd.TotalEnergyKcal > 0 {
			attr(b, "totalEnergyBurned", num(wd.TotalEnergyKcal))
			attr(b, "totalEnergyBurnedUnit", "kcal")
		}
	}
	ex.common(b, s)
	attr(b, "creationDate", ex.date(s.Created))
	attr(b, "startDate", ex.date(s.Start))
	attr(b, "endDate", ex.date(s.End))
	b.WriteString(">\n")
	if !wd.OldStyle {
		flag := "0"
		if wd.IndoorFlag {
			flag = "1"
		}
		s.Meta = append(s.Meta, MetaEntry{"HKIndoorWorkout", flag})
		if wd.ElevationCM > 0 {
			s.Meta = append(s.Meta, MetaEntry{"HKElevationAscended", num(wd.ElevationCM) + " cm"})
		}
		s.Meta = append(s.Meta, MetaEntry{"HKAverageMETs", num(wd.AvgMETs) + " kcal/hr·kg"})
		if !wd.IndoorFlag {
			s.Meta = append(s.Meta, MetaEntry{"HKWeatherTemperature", num(wd.WeatherTempF) + " degF"},
				MetaEntry{"HKWeatherHumidity", num(wd.WeatherHumidity*100) + " %"})
		}
	}
	ex.meta(b, ind+" ", s)
	for _, e := range wd.Events {
		b.WriteString(ind + " <WorkoutEvent")
		attr(b, "type", e.Type)
		attr(b, "date", ex.date(e.At))
		if e.Duration > 0 {
			attr(b, "duration", num(synth.Round(e.Duration.Minutes(), 10)))
			attr(b, "durationUnit", "min")
		}
		b.WriteString("/>\n")
	}
	for _, a := range wd.Activities {
		b.WriteString(ind + " <WorkoutActivity")
		attr(b, "uuid", a.UUID)
		attr(b, "startDate", ex.date(a.Start))
		attr(b, "endDate", ex.date(a.End))
		attr(b, "duration", num(synth.Round(a.End.Sub(a.Start).Minutes(), 10)))
		attr(b, "durationUnit", "min")
		b.WriteString("/>\n")
	}
	for _, st := range wd.Stats {
		b.WriteString(ind + " <WorkoutStatistics")
		attr(b, "type", st.Type)
		attr(b, "startDate", ex.date(st.Start))
		attr(b, "endDate", ex.date(st.End))
		if st.HasAvg {
			attr(b, "average", num(st.Avg))
		}
		if st.HasMin {
			attr(b, "minimum", num(st.Min))
			attr(b, "maximum", num(st.Max))
		}
		if st.HasSum {
			attr(b, "sum", num(synth.Round(st.Sum, 6)))
		}
		attr(b, "unit", st.Unit)
		b.WriteString("/>\n")
	}
	if len(wd.Zones) > 0 {
		b.WriteString(ind + " <WorkoutZoneGroup type=\"HKQuantityTypeIdentifierHeartRate\" source=\"system\" unit=\"count/min\">\n")
		for _, z := range wd.Zones {
			b.WriteString(ind + "  <WorkoutZone")
			if z.Min > 0 {
				attr(b, "minimum", num(z.Min))
			}
			attr(b, "maximum", num(z.Max))
			attr(b, "duration", num(synth.Round(z.Duration.Minutes(), 10)))
			attr(b, "durationUnit", "min")
			b.WriteString("/>\n")
		}
		b.WriteString(ind + " </WorkoutZoneGroup>\n")
	}
	if wd.RoutePath != "" {
		b.WriteString(ind + " <WorkoutRoute")
		ex.common(b, Sample{Src: s.Src})
		attr(b, "creationDate", ex.date(s.Created))
		attr(b, "startDate", ex.date(s.Start))
		attr(b, "endDate", ex.date(s.End))
		b.WriteString(">\n")
		b.WriteString(ind + "  <MetadataEntry key=\"HKMetadataKeySyncVersion\" value=\"2\"/>\n")
		b.WriteString(ind + "  <MetadataEntry key=\"HKMetadataKeySyncIdentifier\" value=\"" + s.UUID + "\"/>\n")
		b.WriteString(ind + "  <FileReference path=\"" + wd.RoutePath + "\"/>\n")
		b.WriteString(ind + " </WorkoutRoute>\n")
		ex.routeRefs = append(ex.routeRefs, routeRef{Path: wd.RoutePath, W: wd.W})
	}
	b.WriteString(ind + "</Workout>\n")
}

func (ex *exporter) summary(d synth.Day) {
	tl := ex.g.TL
	var b strings.Builder
	b.WriteString(" <ActivitySummary")
	attr(&b, "dateComponents", d.Date)
	attr(&b, "activeEnergyBurned", num(synth.Round(tl.ActiveEnergy(d.Start, d.End), 3)))
	attr(&b, "activeEnergyBurnedGoal", "600")
	attr(&b, "activeEnergyBurnedUnit", "kcal")
	var ex2 float64
	for _, w := range tl.Workouts {
		if w.Day == d.Index {
			ex2 += w.Moving().Minutes()
		}
	}
	attr(&b, "appleMoveTime", "0")
	attr(&b, "appleMoveTimeGoal", "0")
	attr(&b, "appleExerciseTime", num(synth.Round(ex2, 0)))
	attr(&b, "appleExerciseTimeGoal", "30")
	attr(&b, "appleStandHours", strconv.Itoa(10+d.Index%4))
	attr(&b, "appleStandHoursGoal", "12")
	b.WriteString("/>\n")
	_, _ = ex.bw.WriteString(b.String())
	ex.stats.Summaries++
}

func (ex *exporter) audiogram() {
	tl := ex.g.TL
	s := tl.Days[0].Start.Add(11 * time.Hour)
	var b strings.Builder
	b.WriteString(" <Audiogram type=\"HKDataTypeIdentifierAudiogram\" sourceName=\"Health\" sourceVersion=\"26.0\"")
	attr(&b, "creationDate", ex.date(s))
	attr(&b, "startDate", ex.date(s))
	attr(&b, "endDate", ex.date(s))
	b.WriteString(">\n  <MetadataEntry key=\"HKWasUserEntered\" value=\"1\"/>\n")
	for i, f := range []float64{250, 500, 1000, 2000, 4000, 8000} {
		fmt.Fprintf(&b, "  <SensitivityPoint frequencyValue=\"%s\" frequencyUnit=\"Hz\" leftEarValue=\"%d\" leftEarUnit=\"dBHL\" rightEarValue=\"%d\" rightEarUnit=\"dBHL\"/>\n",
			num(f), 5+i*2, 10+i*2)
	}
	b.WriteString(" </Audiogram>\n")
	_, _ = ex.bw.WriteString(b.String())
}

func (ex *exporter) clinical() {
	tl := ex.g.TL
	fmt.Fprintf(ex.bw, " <ClinicalRecord type=\"HKClinicalTypeIdentifierImmunizationRecord\" identifier=\"%s\" sourceName=\"Example Clinic\" sourceURL=\"https://fhir.example.org/r4\" fhirVersion=\"4.0.1\" receivedDate=%q resourceFilePath=\"/clinical-records/Immunization-%s.json\"/>\n",
		clinicalID, ex.date(tl.Days[0].Start.Add(12*time.Hour)), clinicalID)
	fmt.Fprintf(ex.bw, " <VisionPrescription type=\"HKVisionPrescriptionTypeGlasses\" dateIssued=%q>\n  <RightEye sphere=\"-1.25\" sphereUnit=\"D\" cylinder=\"-0.5\" cylinderUnit=\"D\" axis=\"90\" axisUnit=\"deg\"/>\n  <LeftEye sphere=\"-1\" sphereUnit=\"D\"/>\n </VisionPrescription>\n",
		ex.date(tl.Days[0].Start.Add(13*time.Hour)))
}

const clinicalID = "00000000-0000-4000-8000-000000000001"

// routes writes one GPX 1.1 file per workout route, as the Health app does.
func (ex *exporter) routes(zw *zip.Writer) error {
	tl := ex.g.TL
	sort.Slice(ex.routeRefs, func(i, j int) bool { return ex.routeRefs[i].Path < ex.routeRefs[j].Path })
	seen := map[string]bool{}
	for i, rf := range ex.routeRefs {
		if seen[rf.Path] {
			continue
		}
		seen[rf.Path] = true
		if ex.o.has(ExportMissingRoute) && i == 0 {
			continue
		}
		name := "apple_health_export" + rf.Path
		f, err := create(zw, name, tl.Opt.Now)
		if err != nil {
			return err
		}
		if ex.o.has(ExportBrokenRoute) && i == 1%len(ex.routeRefs) {
			if _, err := io.WriteString(f, "<gpx><trk><trkseg><trkpt lat=\"north\" lon=\"x\"><time>later</time></trkpt>"); err != nil {
				return err
			}
			continue
		}
		if err := ex.gpx(f, rf); err != nil {
			return err
		}
		ex.stats.Routes++
		if ex.o.has(ExportDuplicateFile) && i == 0 {
			// zip allows two entries of one name; readers differ on which wins.
			if f2, err := create(zw, name, tl.Opt.Now); err == nil {
				_ = ex.gpx(f2, rf)
			}
		}
	}
	return nil
}

func (ex *exporter) gpx(w io.Writer, rf routeRef) error {
	tl := ex.g.TL
	bw := bufio.NewWriter(w)
	lt := rf.W.Start.In(tl.ZoneAt(rf.W.Start))
	fmt.Fprintf(bw, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<gpx version=\"1.1\" creator=\"Apple Health Export\" xmlns=\"http://www.topografix.com/GPX/1/1\" xmlns:xsi=\"http://www.w3.org/2001/XMLSchema-instance\" xsi:schemaLocation=\"http://www.topografix.com/GPX/1/1 http://www.topografix.com/GPX/1/1/gpx.xsd\">\n  <metadata>\n    <time>%s</time>\n  </metadata>\n  <trk>\n    <name>Route %s</name>\n    <trkseg>\n",
		tl.Opt.Now.UTC().Format(time.RFC3339), lt.Format("2006-01-02 3:04pm"))
	step := time.Duration(float64(time.Second) / tl.Opt.Volume)
	if step < 100*time.Millisecond {
		step = 100 * time.Millisecond
	}
	if step < time.Second {
		step = time.Second
	}
	for t := rf.W.Start; t.Before(rf.W.End); t = t.Add(step) {
		if rf.W.Paused(t) {
			continue
		}
		lat, lon, ele, spd := tl.Route(rf.W, t)
		fmt.Fprintf(bw, "      <trkpt lon=\"%.6f\" lat=\"%.6f\"><ele>%.6f</ele><time>%s</time><extensions><speed>%.6f</speed><course>%.6f</course><hAcc>%.6f</hAcc><vAcc>%.6f</vAcc></extensions></trkpt>\n",
			lon, lat, ele, t.UTC().Format(time.RFC3339), spd, float64(t.Unix()%360), 3.0+float64(t.Unix()%3), 2.0+float64(t.Unix()%2))
	}
	_, _ = bw.WriteString("    </trkseg>\n  </trk>\n</gpx>\n")
	return bw.Flush()
}

// ecgs writes one CSV file per recording, named by its local date, a
// suffix for the second of a day.
func (ex *exporter) ecgs(zw *zip.Writer) error {
	tl := ex.g.TL
	perDay := map[string]int{}
	for i, e := range tl.ECGs {
		if !tl.Worn(e.Start) {
			continue
		}
		day := e.Start.In(ex.loc).Format(time.DateOnly)
		name := "apple_health_export/electrocardiograms/ecg_" + day
		if n := perDay[day]; n > 0 {
			name += "_" + strconv.Itoa(n)
		}
		perDay[day]++
		f, err := create(zw, name+".csv", tl.Opt.Now)
		if err != nil {
			return err
		}
		nl := "\n"
		if ex.o.has(ExportCRLF) {
			nl = "\r\n"
		}
		label := ecgClassifications[e.Classification]
		var b strings.Builder
		// The header names the person and their birth date: the invented
		// persona's here.
		fmt.Fprintf(&b, "Name,%s%sDate of Birth,\"%s\"%sRecorded Date,%s%sClassification,%s%sSymptoms,%sSoftware Version,2.0%sDevice,\"%s\"%sSample Rate,%d hertz%s%s%sLead,Lead I%sUnit,µV%s%s",
			tl.P.Name, nl, birth(tl.P.BirthDate), nl, ex.date(e.Start), nl, label, nl, nl, nl, ex.g.Watch.ProductType, nl, e.HZ, nl, nl, nl, nl, nl, nl)
		bad := ex.o.has(ExportBrokenECG) && i == 0
		for k, v := range e.MicroVolts {
			if bad && k == 10 {
				b.WriteString("not a number" + nl)
				continue
			}
			b.WriteString(num(v) + nl)
		}
		if bad {
			b.Reset()
			b.WriteString("Name\nSample Rate,fast\n\n1,2,3\n")
		}
		if _, err := io.WriteString(f, b.String()); err != nil {
			return err
		}
		ex.stats.ECGs++
	}
	return nil
}

func birth(iso string) string {
	t, err := time.Parse(time.DateOnly, iso)
	if err != nil {
		return iso
	}
	return t.Format("Jan 2, 2006")
}

func (ex *exporter) extras(zw *zip.Writer) error {
	tl := ex.g.TL
	f, err := create(zw, "apple_health_export/export_cda.xml", tl.Opt.Now)
	if err != nil {
		return err
	}
	fmt.Fprintf(f, "<?xml version=\"1.0\"?>\n<ClinicalDocument xmlns=\"urn:hl7-org:v3\">\n <realmCode code=\"US\"/>\n <typeId root=\"2.16.840.1.113883.1.3\" extension=\"POCD_HD000040\"/>\n <title>Health Data Export</title>\n <effectiveTime value=\"%s\"/>\n <recordTarget><patientRole><patient><name use=\"CL\">%s</name></patient></patientRole></recordTarget>\n</ClinicalDocument>\n",
		tl.Opt.Now.In(ex.loc).Format("20060102150405-0700"), tl.P.Name)
	c, err := create(zw, "apple_health_export/clinical-records/Immunization-"+clinicalID+".json", tl.Opt.Now)
	if err != nil {
		return err
	}
	res := map[string]any{"resourceType": "Immunization", "id": "example-1", "status": "completed",
		"vaccineCode":        map[string]any{"text": "Influenza vaccine"},
		"occurrenceDateTime": tl.Days[0].Date, "patient": map[string]any{"display": tl.P.Name}}
	if err := json.NewEncoder(c).Encode(res); err != nil {
		return err
	}
	if ex.o.has(ExportMacOSMetadata) {
		m, err := create(zw, "__MACOSX/apple_health_export/._export.xml", tl.Opt.Now)
		if err != nil {
			return err
		}
		_, _ = m.Write([]byte{0, 5, 22, 7, 0, 2, 0, 0})
	}
	return nil
}
