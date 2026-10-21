// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.example

import android.Manifest
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.BackHandler
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.dynamicDarkColorScheme
import androidx.compose.material3.dynamicLightColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import androidx.lifecycle.ViewModelProvider
import kotlinx.coroutines.launch

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        val store = SettingsStore(this)
        val bluetooth = ViewModelProvider(this)[BluetoothModel::class.java]
        setContent {
            ExampleTheme {
                App(store, bluetooth)
            }
        }
    }
}

@Composable
private fun ExampleTheme(content: @Composable () -> Unit) {
    val dark = isSystemInDarkTheme()
    val context = LocalContext.current
    val colors = when {
        Build.VERSION.SDK_INT >= Build.VERSION_CODES.S ->
            if (dark) dynamicDarkColorScheme(context) else dynamicLightColorScheme(context)
        dark -> darkColorScheme()
        else -> lightColorScheme()
    }
    MaterialTheme(colorScheme = colors, content = content)
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun App(store: SettingsStore, bluetooth: BluetoothModel) {
    var showSettings by rememberSaveable { mutableStateOf(store.token.isEmpty()) }
    var serverUrl by remember { mutableStateOf(store.serverUrl) }
    var token by remember { mutableStateOf(store.token) }

    BackHandler(enabled = showSettings && token.isNotEmpty()) { showSettings = false }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text(if (showSettings) "Settings" else "Open Health Sync") },
                actions = {
                    if (showSettings) {
                        TextButton(onClick = { showSettings = false }, enabled = token.isNotEmpty()) { Text("Done") }
                    } else {
                        TextButton(onClick = { showSettings = true }) { Text("Settings") }
                    }
                },
            )
        },
    ) { padding ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
                .verticalScroll(rememberScrollState())
                .padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(16.dp),
        ) {
            if (showSettings) {
                SettingsScreen(
                    serverUrl = serverUrl,
                    token = token,
                    onSave = { url, newToken ->
                        store.serverUrl = url
                        store.token = newToken
                        serverUrl = store.serverUrl
                        token = store.token
                        if (token.isNotEmpty()) showSettings = false
                    },
                )
            } else {
                UploadScreen(serverUrl = serverUrl, token = token)
                BluetoothScreen(bluetooth = bluetooth, serverUrl = serverUrl, token = token)
            }
        }
    }
}

@Composable
private fun SettingsScreen(serverUrl: String, token: String, onSave: (String, String) -> Unit) {
    var url by rememberSaveable { mutableStateOf(serverUrl) }
    var newToken by rememberSaveable { mutableStateOf(token) }

    Text(
        "Paste the server URL and a user token with the upload scope (ohs_ut_...). " +
            "The README shows how to mint one with curl.",
        style = MaterialTheme.typography.bodyMedium,
    )
    OutlinedTextField(
        value = url,
        onValueChange = { url = it },
        label = { Text("Server URL") },
        singleLine = true,
        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri),
        modifier = Modifier.fillMaxWidth(),
    )
    OutlinedTextField(
        value = newToken,
        onValueChange = { newToken = it },
        label = { Text("User token") },
        singleLine = true,
        visualTransformation = PasswordVisualTransformation(),
        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password),
        modifier = Modifier.fillMaxWidth(),
    )
    Button(
        onClick = { onSave(url, newToken) },
        enabled = url.isNotBlank() && newToken.isNotBlank(),
        modifier = Modifier.fillMaxWidth(),
    ) { Text("Save") }
}

@Composable
private fun UploadScreen(serverUrl: String, token: String) {
    val scope = rememberCoroutineScope()
    var bpmText by rememberSaveable { mutableStateOf("") }
    var state by remember { mutableStateOf<UploadState>(UploadState.Idle) }

    val bpm = bpmText.trim().toIntOrNull()
    val bpmValid = bpm != null && bpm in BPM_RANGE
    val running = state is UploadState.Running

    Text("Server: $serverUrl", style = MaterialTheme.typography.bodySmall)
    Text(
        "Enter a heart rate. The app builds one HealthKit-shaped heart rate reading, stamped now in " +
            "this device's time zone, and uploads it with the Kotlin SDK.",
        style = MaterialTheme.typography.bodyMedium,
    )
    OutlinedTextField(
        value = bpmText,
        onValueChange = { bpmText = it.filter(Char::isDigit).take(3) },
        label = { Text("Heart rate (bpm)") },
        singleLine = true,
        isError = bpmText.isNotEmpty() && !bpmValid,
        supportingText = { Text("${BPM_RANGE.first} to ${BPM_RANGE.last}") },
        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number),
        modifier = Modifier.fillMaxWidth(),
    )
    Button(
        onClick = {
            val value = bpm ?: return@Button
            state = UploadState.Running
            scope.launch { state = uploadHeartRate(serverUrl, token, value) }
        },
        enabled = bpmValid && !running && token.isNotEmpty(),
        modifier = Modifier.fillMaxWidth(),
    ) { Text("Upload") }

    when (val s = state) {
        UploadState.Idle -> Unit
        UploadState.Running -> CircularProgressIndicator()
        is UploadState.Done -> ResultCard(s)
        is UploadState.Failed -> ErrorCard(s)
    }
}

/** Android 12 and later ask for the Bluetooth permissions; earlier versions for location. */
private val BLUETOOTH_PERMISSIONS =
    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
        arrayOf(Manifest.permission.BLUETOOTH_SCAN, Manifest.permission.BLUETOOTH_CONNECT)
    } else {
        arrayOf(Manifest.permission.ACCESS_FINE_LOCATION)
    }

@Composable
private fun BluetoothScreen(bluetooth: BluetoothModel, serverUrl: String, token: String) {
    val scope = rememberCoroutineScope()
    val askPermission = rememberLauncherForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) { granted ->
        if (granted.values.all { it }) bluetooth.start() else bluetooth.refused()
    }
    val state = bluetooth.state

    HorizontalDivider()
    Text("Bluetooth devices", style = MaterialTheme.typography.titleMedium)
    Text(
        "Connects to heart rate straps, blood pressure cuffs, scales, glucose meters, thermometers " +
            "and pulse oximeters that use the standard Bluetooth health profiles, and uploads the " +
            "values they send with the Kotlin SDK. The server decodes them.",
        style = MaterialTheme.typography.bodyMedium,
    )
    Button(
        onClick = { if (bluetooth.scanning) bluetooth.stop() else askPermission.launch(BLUETOOTH_PERMISSIONS) },
        modifier = Modifier.fillMaxWidth(),
    ) { Text(if (bluetooth.scanning) "Stop" else "Find devices") }
    if (bluetooth.live.isNotEmpty()) {
        Text(bluetooth.live, style = MaterialTheme.typography.bodyLarge)
    }
    Text("Values recorded: ${bluetooth.recorded}", style = MaterialTheme.typography.bodySmall)
    Button(
        onClick = { scope.launch { bluetooth.upload(serverUrl, token) } },
        enabled = bluetooth.canUpload && state !is UploadState.Running && token.isNotEmpty(),
        modifier = Modifier.fillMaxWidth(),
    ) { Text("Upload recorded values") }

    when (state) {
        UploadState.Idle -> Unit
        UploadState.Running -> CircularProgressIndicator()
        is UploadState.Done -> ResultCard(state)
        is UploadState.Failed -> ErrorCard(state)
    }
}

@Composable
private fun ResultCard(done: UploadState.Done) {
    Card(modifier = Modifier.fillMaxWidth()) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text("Uploaded", style = MaterialTheme.typography.titleMedium)
            done.parts.forEach { part ->
                Text(
                    "Part ${part.partNumber}: ${part.type}, day ${part.day}, " +
                        (if (part.duplicate) "duplicate (already stored)" else "stored"),
                    style = MaterialTheme.typography.bodyMedium,
                )
            }
            Text(
                "The server processes it in the background. Check it with the curl commands in the README.",
                style = MaterialTheme.typography.bodySmall,
            )
        }
    }
}

@Composable
private fun ErrorCard(failed: UploadState.Failed) {
    Card(modifier = Modifier.fillMaxWidth()) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text("Upload failed", style = MaterialTheme.typography.titleMedium, color = MaterialTheme.colorScheme.error)
            Text(failed.message, style = MaterialTheme.typography.bodyMedium)
            failed.status?.let { Text("Status: $it", style = MaterialTheme.typography.bodyMedium) }
            failed.title?.let { Text("Title: $it", style = MaterialTheme.typography.bodyMedium) }
            failed.requestId?.let { Text("Request id: $it", style = MaterialTheme.typography.bodyMedium) }
        }
    }
}
