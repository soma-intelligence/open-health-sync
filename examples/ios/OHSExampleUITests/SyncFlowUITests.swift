// SPDX-License-Identifier: AGPL-3.0-only

import XCTest

/// The whole app against a live server, on a simulator: Settings, the Health access sheet, a
/// first sync that uploads the seeded heart rates and a second sync that finds nothing new.
///
/// test/e2e/apps.sh runs it and then reads the samples back with the tenant key. xcodebuild
/// passes it three variables (as TEST_RUNNER_OHS_URL and so on), and it is skipped without them:
///   OHS_URL    the server as the simulator sees it, for example http://localhost:8080
///   OHS_TOKEN  a user token with the upload scope
///   OHS_BPM    comma separated heart rates the app writes to Health before syncing
final class SyncFlowUITests: XCTestCase {
    private let timeout: TimeInterval = 60

    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    func testSettingsAccessAndTwoSyncs() throws {
        let env = ProcessInfo.processInfo.environment
        guard let url = env["OHS_URL"], let token = env["OHS_TOKEN"], let bpm = env["OHS_BPM"],
              !url.isEmpty, !token.isEmpty, !bpm.isEmpty
        else {
            throw XCTSkip("needs OHS_URL, OHS_TOKEN and OHS_BPM (test/e2e/apps.sh)")
        }

        let app = XCUIApplication()
        app.launchEnvironment["OHS_UI_TEST_SEED_BPM"] = bpm
        app.launch()

        // Settings: replace the URL, paste the token, save.
        app.buttons["Settings"].tap()
        let urlField = app.textFields["serverURL"]
        XCTAssertTrue(urlField.waitForExistence(timeout: timeout))
        urlField.tap()
        let current = urlField.value as? String ?? ""
        urlField.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: current.count))
        urlField.typeText(url)
        let tokenField = app.secureTextFields["userToken"]
        tokenField.tap()
        tokenField.typeText(token)
        app.buttons["Save"].tap()
        waitUntilGone(urlField, app)
        XCTAssertFalse(app.staticTexts["Add the server URL and a user token first."].exists, dump(app))

        // Health access: the system sheet, every type on.
        app.buttons["Allow Health access"].tap()
        tapInHealthSheet(app, "Turn On All")
        tapInHealthSheet(app, "Allow")
        XCTAssertTrue(scrollUntilExists(element(app, startingWith: "Health access requested"), in: app), dump(app))

        // First sync: the seeded readings go up.
        tapSyncNow(app)
        XCTAssertTrue(scrollUntilExists(element(app, startingWith: "Lines uploaded"), in: app), dump(app))
        XCTAssertFalse(app.staticTexts["Nothing new since the last sync."].exists, dump(app))

        // Second sync: the anchors hold, nothing is sent again.
        tapSyncNow(app)
        XCTAssertTrue(scrollUntilExists(app.staticTexts["Nothing new since the last sync."], in: app), dump(app))
    }

    /// The Health sheet is a system view shown over the app; look for the control in the app and
    /// in SpringBoard, since the host process differs between iOS versions.
    private func tapInHealthSheet(_ app: XCUIApplication, _ label: String) {
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            for root in [app, springboard] {
                let control = root.descendants(matching: .any)
                    .matching(NSPredicate(format: "label == %@ OR identifier == %@", label, label)).firstMatch
                if control.exists && control.isHittable {
                    control.tap()
                    return
                }
            }
            Thread.sleep(forTimeInterval: 0.5)
        }
        XCTFail("no \"\(label)\" in the Health sheet\n" + dump(app))
    }

    /// The list is lazy: rows off screen are not in the accessibility tree until shown. Swipes
    /// between checks (toward the end of the list, or back toward its top) until `element` is
    /// on screen or the timeout passes.
    private func scrollUntilExists(_ element: XCUIElement, in app: XCUIApplication, towardTop: Bool = false) -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if element.waitForExistence(timeout: 2) && element.isHittable {
                return true
            }
            let list = app.collectionViews.firstMatch
            if towardTop {
                list.swipeDown()
            } else {
                list.swipeUp()
            }
        }
        return element.exists
    }

    private func tapSyncNow(_ app: XCUIApplication) {
        let sync = app.buttons["Sync now"]
        XCTAssertTrue(scrollUntilExists(sync, in: app, towardTop: true), dump(app))
        sync.tap()
    }

    /// The first element whose label starts with `prefix`: a SwiftUI row such as LabeledContent
    /// reads as its title followed by its value.
    private func element(_ app: XCUIApplication, startingWith prefix: String) -> XCUIElement {
        return app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", prefix)).firstMatch
    }

    private func waitUntilGone(_ element: XCUIElement, _ app: XCUIApplication) {
        let gone = XCTNSPredicateExpectation(predicate: NSPredicate(format: "exists == false"), object: element)
        if XCTWaiter.wait(for: [gone], timeout: timeout) != .completed {
            XCTFail("still on screen: \(element)\n" + dump(app))
        }
    }

    /// The accessibility tree, for a failure message. The token field is a secure field: its
    /// value never appears.
    private func dump(_ app: XCUIApplication) -> String {
        return app.debugDescription
    }
}
