// SPDX-License-Identifier: AGPL-3.0-only

import OpenHealthSyncHealthKit
import SwiftUI

/// The main screen: the types to sync, the three actions and the last results.
struct ContentView: View {
    @EnvironmentObject private var model: AppModel
    @State private var showSettings = false

    var body: some View {
        NavigationStack {
            List {
                if !model.hasToken {
                    Section {
                        Text("Add the server URL and a user token first.")
                        Button("Open Settings") { showSettings = true }
                    }
                }

                Section {
                    ForEach(HealthDataType.all) { type in
                        Toggle(type.name, isOn: Binding(
                            get: { model.isSelected(type) },
                            set: { model.setSelected(type, $0) }))
                    }
                } header: {
                    Text("Health data to sync")
                }

                Section {
                    Button("Allow Health access") {
                        Task { await model.requestHealthAccess() }
                    }
                    Button("Sync now") {
                        Task { await model.syncNow() }
                    }
                    Toggle("Background delivery", isOn: Binding(
                        get: { model.backgroundDelivery },
                        set: { on in Task { await model.setBackgroundDelivery(on) } }))
                } footer: {
                    Text("Server: \(model.serverURL)")
                }
                .disabled(model.isBusy)

                if model.isBusy {
                    Section {
                        HStack(spacing: 12) {
                            ProgressView()
                            Text("Working...")
                        }
                    }
                }

                if let notice = model.notice {
                    Section {
                        Text(notice)
                    }
                }

                if let outcome = model.lastSync {
                    Section {
                        OutcomeView(outcome: outcome)
                    } header: {
                        Text("Last sync")
                    }
                }

                if let outcome = model.lastBackgroundSync {
                    Section {
                        OutcomeView(outcome: outcome)
                    } header: {
                        Text("Last background sync")
                    }
                }
            }
            .navigationTitle("OHS Example")
            .toolbar {
                ToolbarItem(placement: .primaryAction) {
                    Button {
                        showSettings = true
                    } label: {
                        Image(systemName: "gearshape")
                    }
                    .accessibilityLabel("Settings")
                }
            }
            .sheet(isPresented: $showSettings) {
                SettingsView()
                    .environmentObject(model)
            }
        }
    }
}

/// Shows a `SyncReport`, or the error of a failed sync.
struct OutcomeView: View {
    let outcome: SyncOutcome

    var body: some View {
        LabeledContent("Time") {
            Text(outcome.date, style: .time)
        }
        if let report = outcome.report {
            LabeledContent("Lines uploaded", value: "\(report.linesUploaded)")
            LabeledContent("Parts uploaded", value: "\(report.partsUploaded)")
            LabeledContent("Parts already stored", value: "\(report.duplicateParts)")
            LabeledContent("Samples skipped", value: "\(report.skippedSamples)")
            ForEach(report.linesByType.sorted(by: { $0.key < $1.key }), id: \.key) { entry in
                LabeledContent(HealthDataType.name(forIdentifier: entry.key), value: "\(entry.value) lines")
            }
            ForEach(report.batchIds, id: \.self) { batch in
                LabeledContent("Batch") {
                    Text(batch)
                        .font(.caption.monospaced())
                        .textSelection(.enabled)
                }
            }
            if report.partsUploaded == 0 {
                Text("Nothing new since the last sync.")
                    .foregroundStyle(.secondary)
            }
        } else {
            ForEach(outcome.errorLines, id: \.self) { line in
                Text(line)
                    .foregroundStyle(.red)
                    .textSelection(.enabled)
            }
        }
    }
}
