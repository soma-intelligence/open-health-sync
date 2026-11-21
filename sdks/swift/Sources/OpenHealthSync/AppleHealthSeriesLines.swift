// SPDX-License-Identifier: AGPL-3.0-only

import Foundation

// The lines of the HealthKit types that are more than one value: workout routes, ECG
// recordings, heartbeat series, audiograms and states of mind, in the shapes the server's
// connector reads (connectors/applehealth: route.go, ecg.go, heartbeat.go, audiogram.go,
// stateofmind.go). Like AppleHealthLines.swift, these hold plain values.

/// One GPS point of a workout route. The server attaches the points of a part to the one
/// workout line of that part, so a route part starts with its workout's line
/// (`WorkoutLine`); points alone are attached to a stored workout that overlaps them in time.
public struct RoutePointLine: Sendable, Equatable {
    public var timestamp: Date
    /// Degrees.
    public var latitude: Double
    public var longitude: Double
    /// Metres.
    public var altitude: Double?
    /// Metres per second.
    public var speed: Double?
    /// Degrees from north.
    public var course: Double?

    public init(
        timestamp: Date, latitude: Double, longitude: Double,
        altitude: Double? = nil, speed: Double? = nil, course: Double? = nil
    ) {
        self.timestamp = timestamp
        self.latitude = latitude
        self.longitude = longitude
        self.altitude = altitude
        self.speed = speed
        self.course = course
    }

    public func jsonObject() -> JSONObject {
        var line: JSONObject = [
            "timestamp": .string(RFC3339.format(timestamp)),
            "latitude": .number(latitude),
            "longitude": .number(longitude),
        ]
        if let v = altitude { line["altitude"] = .number(v) }
        if let v = speed { line["speed"] = .number(v) }
        if let v = course { line["course"] = .number(v) }
        return line
    }
}

/// An ECG recording, without its voltages: the `HKElectrocardiogram` classification and
/// symptoms status raw values, the sampling frequency in hertz, the heart rate in beats per
/// minute. Its voltage lines (`ECGVoltageLine`) go in the same part, after it: the server
/// attaches the voltages of a part to the one recording line of that part.
public struct ECGLine: AppleHealthLine, Equatable {
    public var startDate: Date
    public var endDate: Date
    /// `HKElectrocardiogram.Classification` raw value.
    public var classification: Int
    /// `HKElectrocardiogram.SymptomsStatus` raw value.
    public var symptomsStatus: Int
    /// Beats per minute.
    public var averageHeartRate: Double?
    /// Hertz.
    public var samplingFrequency: Double?
    public var numberOfVoltageMeasurements: Int
    public var provenance: LineProvenance
    public var metadata: JSONObject

    public init(
        startDate: Date, endDate: Date, classification: Int, symptomsStatus: Int = 0,
        averageHeartRate: Double? = nil, samplingFrequency: Double? = nil,
        numberOfVoltageMeasurements: Int = 0,
        provenance: LineProvenance = LineProvenance(), metadata: JSONObject = [:]
    ) {
        self.startDate = startDate
        self.endDate = endDate
        self.classification = classification
        self.symptomsStatus = symptomsStatus
        self.averageHeartRate = averageHeartRate
        self.samplingFrequency = samplingFrequency
        self.numberOfVoltageMeasurements = numberOfVoltageMeasurements
        self.provenance = provenance
        self.metadata = metadata
    }

    public var typeIdentifier: String { AppleHealthTypes.electrocardiogram }
    public var timeZoneIdentifier: String? { metadata["HKTimeZone"]?.stringValue }

    public func jsonObject() -> JSONObject {
        var line = commonFields(startDate: startDate, endDate: endDate, provenance: provenance, metadata: metadata)
        line["classification"] = .number(Double(classification))
        line["symptomsStatus"] = .number(Double(symptomsStatus))
        line["numberOfVoltageMeasurements"] = .number(Double(numberOfVoltageMeasurements))
        if let v = averageHeartRate { line["averageHeartRate"] = .number(v) }
        if let v = samplingFrequency { line["samplingFrequency"] = .number(v) }
        return line
    }
}

/// One voltage measurement of an ECG recording.
public struct ECGVoltageLine: Sendable, Equatable {
    /// Seconds from the recording's start.
    public var timeSinceSampleStart: Double
    /// Volts (the server stores microvolts).
    public var volts: Double

    public init(timeSinceSampleStart: Double, volts: Double) {
        self.timeSinceSampleStart = timeSinceSampleStart
        self.volts = volts
    }

    public func jsonObject() -> JSONObject {
        return ["timeSinceSampleStart": .number(timeSinceSampleStart), "value": .number(volts)]
    }
}

/// One beat of a heartbeat series.
public struct Heartbeat: Sendable, Equatable {
    /// Seconds from the series' start.
    public var timeSinceSeriesStart: Double
    /// Whether data is missing between the previous beat and this one.
    public var precededByGap: Bool

    public init(timeSinceSeriesStart: Double, precededByGap: Bool = false) {
        self.timeSinceSeriesStart = timeSinceSeriesStart
        self.precededByGap = precededByGap
    }
}

/// A heartbeat series (`HKHeartbeatSeriesSample`): the whole series in one line, as the
/// server computes an RR interval from two consecutive beats of a line.
public struct HeartbeatSeriesLine: AppleHealthLine, Equatable {
    public var startDate: Date
    public var endDate: Date
    public var beats: [Heartbeat]
    public var provenance: LineProvenance
    public var metadata: JSONObject

    public init(
        startDate: Date, endDate: Date, beats: [Heartbeat],
        provenance: LineProvenance = LineProvenance(), metadata: JSONObject = [:]
    ) {
        self.startDate = startDate
        self.endDate = endDate
        self.beats = beats
        self.provenance = provenance
        self.metadata = metadata
    }

    public var typeIdentifier: String { AppleHealthTypes.heartbeatSeries }
    public var timeZoneIdentifier: String? { metadata["HKTimeZone"]?.stringValue }

    public func jsonObject() -> JSONObject {
        var line = commonFields(startDate: startDate, endDate: endDate, provenance: provenance, metadata: metadata)
        line["heartbeats"] = .array(beats.map { beat in
            JSONValue.object([
                "timeSinceSeriesStart": .number(beat.timeSinceSeriesStart),
                "precededByGap": .bool(beat.precededByGap),
            ])
        })
        return line
    }

    /// The series as lines of at most `maxBeats` beats, for a series too long for one line.
    /// Every line keeps the series' start and UUID, and each line after the first starts
    /// with the last beat of the line before it, so the interval across the cut is still
    /// computed and no interval is counted twice.
    public func split(maxBeats: Int) -> [HeartbeatSeriesLine] {
        let size = max(2, maxBeats)
        guard beats.count > size else {
            return [self]
        }
        var lines: [HeartbeatSeriesLine] = []
        var from = 0
        while from < beats.count - 1 || lines.isEmpty {
            let to = min(beats.count, from + size)
            var line = self
            line.beats = Array(beats[from..<to])
            if from > 0 {
                // The repeated beat only anchors the next interval; the gap before it, if
                // any, was already accounted for in the line before.
                line.beats[0].precededByGap = false
            }
            lines.append(line)
            if to == beats.count {
                break
            }
            from = to - 1
        }
        return lines
    }
}

/// One tested frequency of an audiogram.
public struct AudiogramPoint: Sendable, Equatable {
    /// Hertz.
    public var frequency: Double
    /// dBHL.
    public var leftEarSensitivity: Double?
    public var rightEarSensitivity: Double?

    public init(frequency: Double, leftEarSensitivity: Double? = nil, rightEarSensitivity: Double? = nil) {
        self.frequency = frequency
        self.leftEarSensitivity = leftEarSensitivity
        self.rightEarSensitivity = rightEarSensitivity
    }

    func jsonObject() -> JSONObject {
        var point: JSONObject = ["frequency": .number(frequency)]
        if let v = leftEarSensitivity { point["leftEarSensitivity"] = .number(v) }
        if let v = rightEarSensitivity { point["rightEarSensitivity"] = .number(v) }
        return point
    }
}

/// An audiogram (`HKAudiogramSample`). The server keeps the sensitivity points as they
/// are, in the sample's metadata.
public struct AudiogramLine: AppleHealthLine, Equatable {
    public var startDate: Date
    public var endDate: Date
    public var sensitivityPoints: [AudiogramPoint]
    public var provenance: LineProvenance
    public var metadata: JSONObject

    public init(
        startDate: Date, endDate: Date, sensitivityPoints: [AudiogramPoint],
        provenance: LineProvenance = LineProvenance(), metadata: JSONObject = [:]
    ) {
        self.startDate = startDate
        self.endDate = endDate
        self.sensitivityPoints = sensitivityPoints
        self.provenance = provenance
        self.metadata = metadata
    }

    /// The `quantity_type` the server recognizes the line by.
    public static let discriminator = "HKDataTypeIdentifierAudiogramSample"

    public var typeIdentifier: String { AppleHealthTypes.audiogram }
    public var timeZoneIdentifier: String? { metadata["HKTimeZone"]?.stringValue }

    public func jsonObject() -> JSONObject {
        var line = commonFields(startDate: startDate, endDate: endDate, provenance: provenance, metadata: metadata)
        line["quantity_type"] = .string(AudiogramLine.discriminator)
        line["_raw"] = .object([
            "sensitivityPoints": .array(sensitivityPoints.map { JSONValue.object($0.jsonObject()) }),
        ])
        return line
    }
}

/// A state of mind (`HKStateOfMind`): the raw values of its kind, labels and associations,
/// and its valence from -1 to 1.
public struct StateOfMindLine: AppleHealthLine, Equatable {
    public var startDate: Date
    public var endDate: Date
    /// `HKStateOfMind.Kind` raw value.
    public var kind: Int
    public var valence: Double
    /// `HKStateOfMind.Label` raw values.
    public var labels: [Int]
    /// `HKStateOfMind.Association` raw values.
    public var associations: [Int]
    public var provenance: LineProvenance
    public var metadata: JSONObject

    /// The `quantity_type` the server recognizes the line by.
    public static let discriminator = "HKStateOfMindType"

    public init(
        startDate: Date, endDate: Date, kind: Int, valence: Double,
        labels: [Int] = [], associations: [Int] = [],
        provenance: LineProvenance = LineProvenance(), metadata: JSONObject = [:]
    ) {
        self.startDate = startDate
        self.endDate = endDate
        self.kind = kind
        self.valence = valence
        self.labels = labels
        self.associations = associations
        self.provenance = provenance
        self.metadata = metadata
    }

    public var typeIdentifier: String { AppleHealthTypes.stateOfMind }
    public var timeZoneIdentifier: String? { metadata["HKTimeZone"]?.stringValue }

    public func jsonObject() -> JSONObject {
        var line = commonFields(startDate: startDate, endDate: endDate, provenance: provenance, metadata: metadata)
        line["quantity_type"] = .string(StateOfMindLine.discriminator)
        var raw: JSONObject = ["kind": .number(Double(kind)), "valence": .number(valence)]
        if !labels.isEmpty {
            raw["labels"] = .array(labels.map { JSONValue.number(Double($0)) })
        }
        if !associations.isEmpty {
            raw["associations"] = .array(associations.map { JSONValue.number(Double($0)) })
        }
        line["_raw"] = .object(raw)
        return line
    }
}
