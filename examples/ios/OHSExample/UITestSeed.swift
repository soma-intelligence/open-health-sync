// SPDX-License-Identifier: AGPL-3.0-only

#if DEBUG
import Foundation
import HealthKit

/// UI test support, compiled into debug builds only.
///
/// The simulator's Health store starts empty and has no command to add data. When the UI test
/// launches the app with `OHS_UI_TEST_SEED_BPM` (comma separated heart rates), "Allow Health
/// access" also asks to write heart rate and saves one reading per value, a minute apart, the
/// last one now. Without that variable, and in every release build, the app never writes to
/// Health.
enum UITestSeed {
    static let environmentKey = "OHS_UI_TEST_SEED_BPM"

    /// The heart rates to write, or nil when the app was not launched by the UI test.
    static var heartRates: [Double]? {
        guard let raw = ProcessInfo.processInfo.environment[environmentKey] else {
            return nil
        }
        let values = raw.split(separator: ",").compactMap { Double($0.trimmingCharacters(in: .whitespaces)) }
        return values.isEmpty ? nil : values
    }

    /// Asks for write access to heart rate together with read access to `read`, in one sheet,
    /// then saves the readings. Throws when HealthKit refuses the save (access declined).
    static func authorizeAndWrite(_ heartRates: [Double], store: HKHealthStore, read: Set<HKSampleType>) async throws {
        let heartRate = HKQuantityType(.heartRate)
        try await store.requestAuthorization(toShare: [heartRate], read: Set<HKObjectType>(read.map { $0 as HKObjectType }))
        let unit = HKUnit.count().unitDivided(by: .minute())
        let now = Date()
        let samples = heartRates.enumerated().map { index, value in
            let date = now.addingTimeInterval(TimeInterval(index - heartRates.count + 1) * 60)
            return HKQuantitySample(
                type: heartRate,
                quantity: HKQuantity(unit: unit, doubleValue: value),
                start: date,
                end: date)
        }
        try await store.save(samples)
    }
}
#endif
