// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"strconv"
	"strings"
	"time"
)

// The export.xml elements this package reads. Only the attributes that end
// up in a line are declared; encoding/xml ignores the others.

type xmlMeta struct {
	Key   string `xml:"key,attr"`
	Value string `xml:"value,attr"`
}

type commonAttrs struct {
	SourceName    string `xml:"sourceName,attr"`
	SourceVersion string `xml:"sourceVersion,attr"`
	Device        string `xml:"device,attr"`
	StartDate     string `xml:"startDate,attr"`
	EndDate       string `xml:"endDate,attr"`
}

type xmlRecord struct {
	commonAttrs
	Type       string    `xml:"type,attr"`
	Unit       string    `xml:"unit,attr"`
	Value      string    `xml:"value,attr"`
	Metadata   []xmlMeta `xml:"MetadataEntry"`
	Heartbeats *struct {
		Beats []struct {
			Time string `xml:"time,attr"`
		} `xml:"InstantaneousBeatsPerMinute"`
	} `xml:"HeartbeatSeries"`
}

type xmlCorrelation struct {
	commonAttrs
	Type     string      `xml:"type,attr"`
	Metadata []xmlMeta   `xml:"MetadataEntry"`
	Records  []xmlRecord `xml:"Record"`
}

type xmlWorkout struct {
	commonAttrs
	ActivityType          string    `xml:"workoutActivityType,attr"`
	Duration              string    `xml:"duration,attr"`
	DurationUnit          string    `xml:"durationUnit,attr"`
	TotalDistance         string    `xml:"totalDistance,attr"`
	TotalDistanceUnit     string    `xml:"totalDistanceUnit,attr"`
	TotalEnergyBurned     string    `xml:"totalEnergyBurned,attr"`
	TotalEnergyBurnedUnit string    `xml:"totalEnergyBurnedUnit,attr"`
	Metadata              []xmlMeta `xml:"MetadataEntry"`
	Events                []struct {
		Type         string `xml:"type,attr"`
		Date         string `xml:"date,attr"`
		Duration     string `xml:"duration,attr"`
		DurationUnit string `xml:"durationUnit,attr"`
	} `xml:"WorkoutEvent"`
	Stats []struct {
		Type    string `xml:"type,attr"`
		Average string `xml:"average,attr"`
		Minimum string `xml:"minimum,attr"`
		Maximum string `xml:"maximum,attr"`
		Sum     string `xml:"sum,attr"`
		Unit    string `xml:"unit,attr"`
	} `xml:"WorkoutStatistics"`
	Routes []struct {
		File struct {
			Path string `xml:"path,attr"`
		} `xml:"FileReference"`
	} `xml:"WorkoutRoute"`
}

type xmlAudiogram struct {
	commonAttrs
	Metadata []xmlMeta `xml:"MetadataEntry"`
	Points   []struct {
		Frequency string `xml:"frequencyValue,attr"`
		Left      string `xml:"leftEarValue,attr"`
		Right     string `xml:"rightEarValue,attr"`
	} `xml:"SensitivityPoint"`
}

// gpxPoint is one trkpt of a workout route GPX file.
type gpxPoint struct {
	Lat  string `xml:"lat,attr"`
	Lon  string `xml:"lon,attr"`
	Ele  string `xml:"ele"`
	Time string `xml:"time"`
	Ext  struct {
		Speed  string `xml:"speed"`
		Course string `xml:"course"`
		HAcc   string `xml:"hAcc"`
		VAcc   string `xml:"vAcc"`
	} `xml:"extensions"`
}

// line is the route point line of the upload contract. ok is false without
// a time or a position.
func (p gpxPoint) line() (map[string]any, bool) {
	ts, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(p.Time))
	if err != nil {
		return nil, false
	}
	lat, err1 := strconv.ParseFloat(strings.TrimSpace(p.Lat), 64)
	lon, err2 := strconv.ParseFloat(strings.TrimSpace(p.Lon), 64)
	if err1 != nil || err2 != nil {
		return nil, false
	}
	f := map[string]any{"timestamp": wireTime(ts), "latitude": lat, "longitude": lon}
	for field, raw := range map[string]string{
		"altitude": p.Ele, "speed": p.Ext.Speed, "course": p.Ext.Course,
		"horizontalAccuracy": p.Ext.HAcc, "verticalAccuracy": p.Ext.VAcc,
	} {
		if v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil {
			f[field] = v
		}
	}
	return f, true
}
