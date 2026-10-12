// SPDX-License-Identifier: AGPL-3.0-only

import SwiftUI
import UIKit

/// Turns HealthKit background delivery back on at every launch, as HealthKit requires.
@MainActor
final class AppDelegate: NSObject, UIApplicationDelegate {
    func application(
        _ application: UIApplication,
        didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil
    ) -> Bool {
        AppModel.shared.resumeBackgroundDelivery()
        return true
    }
}

@main
struct ExampleApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate

    var body: some Scene {
        WindowGroup {
            ContentView()
                .environmentObject(AppModel.shared)
        }
    }
}
