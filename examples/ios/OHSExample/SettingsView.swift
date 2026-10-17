// SPDX-License-Identifier: AGPL-3.0-only

import SwiftUI

/// Server URL and user token. The token goes to the Keychain and is never shown again.
struct SettingsView: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.dismiss) private var dismiss
    @State private var serverURL = ""
    @State private var token = ""
    @State private var errorMessage: String?

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField(AppModel.defaultServerURL, text: $serverURL)
                        .accessibilityIdentifier("serverURL")
                        .keyboardType(.URL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                } header: {
                    Text("Server URL")
                } footer: {
                    Text("On the simulator, http://localhost:8080 reaches the server on your Mac.")
                }

                Section {
                    SecureField(model.hasToken ? "Saved. Paste a new one to replace it" : "ohs_ut_...", text: $token)
                        .accessibilityIdentifier("userToken")
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                    if model.hasToken {
                        Button("Remove token", role: .destructive) {
                            model.removeToken()
                        }
                    }
                } header: {
                    Text("User token")
                } footer: {
                    Text("A user token with the upload scope, stored in the Keychain. Never the tenant key.")
                }

                if let errorMessage = errorMessage {
                    Section {
                        Text(errorMessage)
                            .foregroundStyle(.red)
                    }
                }

                Section {
                    Button("Reset sync anchors") {
                        model.resetAnchors()
                    }
                } footer: {
                    Text("The next sync then uploads the whole history again, for example after pointing the app at a new server.")
                }
            }
            .navigationTitle("Settings")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Save") { save() }
                }
            }
            .onAppear {
                serverURL = model.serverURL
            }
        }
    }

    private func save() {
        if let message = model.saveSettings(serverURL: serverURL, token: token) {
            errorMessage = message
        } else {
            token = ""
            dismiss()
        }
    }
}
