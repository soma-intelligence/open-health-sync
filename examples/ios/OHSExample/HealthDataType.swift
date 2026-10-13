// SPDX-License-Identifier: AGPL-3.0-only

import HealthKit

/// One HealthKit type the app can sync.
struct HealthDataType: Identifiable {
    /// The HealthKit identifier, also the key of `SyncReport.linesByType`.
    let id: String
    let name: String
    let sampleType: HKSampleType

    init(name: String, sampleType: HKSampleType) {
        self.id = sampleType.identifier
        self.name = name
        self.sampleType = sampleType
    }

    static let all: [HealthDataType] = [
        HealthDataType(name: "Heart rate", sampleType: HKQuantityType(.heartRate)),
        HealthDataType(name: "Steps", sampleType: HKQuantityType(.stepCount)),
        HealthDataType(name: "Active energy", sampleType: HKQuantityType(.activeEnergyBurned)),
        HealthDataType(name: "Sleep", sampleType: HKCategoryType(.sleepAnalysis)),
        HealthDataType(name: "Workouts", sampleType: HKObjectType.workoutType()),
    ]

    static func name(forIdentifier identifier: String) -> String {
        return all.first(where: { $0.id == identifier })?.name ?? identifier
    }
}
