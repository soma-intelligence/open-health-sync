// SPDX-License-Identifier: AGPL-3.0-only

import { StatusBar } from "expo-status-bar";
import * as SecureStore from "expo-secure-store";
import { useEffect, useState } from "react";
import {
  ActivityIndicator,
  Button,
  SafeAreaView,
  ScrollView,
  StyleSheet,
  Text,
  TextInput,
  View,
} from "react-native";
import {
  AppleHealthPartBuilder,
  OhsApiError,
  OhsClient,
  localDay,
  newBatchId,
  type BuiltPart,
} from "open-health-sync-react-native";

const HEART_RATE = "HKQuantityTypeIdentifierHeartRate";
const TOKEN_KEY = "ohs_user_token";
const URL_KEY = "ohs_server_url";
const DEFAULT_URL = "http://localhost:8080";

interface PartRow {
  partNumber: number;
  type: string;
  day: string;
  duplicate: boolean;
}

interface ErrorInfo {
  status?: number;
  title?: string;
  requestId?: string;
  message: string;
}

function describeError(e: unknown): ErrorInfo {
  if (e instanceof OhsApiError) {
    const info: ErrorInfo = { status: e.status, message: e.detail ?? "The server refused the upload." };
    if (e.title !== undefined) info.title = e.title;
    if (e.requestId !== undefined) info.requestId = e.requestId;
    return info;
  }
  return { message: e instanceof Error ? e.message : "Unknown error" };
}

// The client asks for the token before every request, so a token saved later is picked up
// without rebuilding the client. The token lives only in SecureStore.
function makeClient(baseUrl: string): OhsClient {
  return new OhsClient({
    baseUrl,
    tokenProvider: async () => {
      const token = await SecureStore.getItemAsync(TOKEN_KEY);
      if (!token) throw new Error("No user token saved. Paste one and tap Save.");
      return token;
    },
  });
}

export default function App() {
  const [url, setUrl] = useState(DEFAULT_URL);
  const [tokenInput, setTokenInput] = useState("");
  const [hasToken, setHasToken] = useState(false);
  const [bpm, setBpm] = useState("72");
  const [busy, setBusy] = useState(false);
  const [rows, setRows] = useState<PartRow[]>([]);
  const [error, setError] = useState<ErrorInfo | null>(null);
  const [lastParts, setLastParts] = useState<BuiltPart[] | null>(null);

  useEffect(() => {
    (async () => {
      const savedUrl = await SecureStore.getItemAsync(URL_KEY);
      if (savedUrl) setUrl(savedUrl);
      setHasToken((await SecureStore.getItemAsync(TOKEN_KEY)) !== null);
    })().catch(() => setError({ message: "Could not read the secure store." }));
  }, []);

  async function save() {
    setError(null);
    try {
      await SecureStore.setItemAsync(URL_KEY, url.trim());
      if (tokenInput.trim() !== "") {
        await SecureStore.setItemAsync(TOKEN_KEY, tokenInput.trim());
        setTokenInput("");
        setHasToken(true);
      }
    } catch (e) {
      setError(describeError(e));
    }
  }

  async function forgetToken() {
    await SecureStore.deleteItemAsync(TOKEN_KEY);
    setHasToken(false);
  }

  async function send(parts: BuiltPart[]) {
    setBusy(true);
    setError(null);
    setRows([]);
    try {
      const results = await makeClient(url.trim()).uploadParts(parts);
      setRows(
        parts.map((p, i) => ({
          partNumber: p.partNumber,
          type: p.type,
          day: p.day,
          duplicate: results[i]?.duplicate ?? false,
        })),
      );
      setLastParts(parts);
    } catch (e) {
      setError(describeError(e));
    } finally {
      setBusy(false);
    }
  }

  function upload() {
    const value = Number(bpm);
    if (!Number.isFinite(value) || value <= 0) {
      setError({ message: "Enter a heart rate in beats per minute." });
      return;
    }
    const timeZone = Intl.DateTimeFormat().resolvedOptions().timeZone;
    const now = new Date();
    const builder = new AppleHealthPartBuilder(newBatchId("rn-example"));
    builder.add(HEART_RATE, localDay(now, timeZone), {
      quantityType: HEART_RATE,
      startDate: now.toISOString(),
      endDate: now.toISOString(),
      value,
      unit: "count/min",
      sourceName: "OHS React Native example",
      metadata: { HKTimeZone: timeZone },
    });
    void send(builder.build());
  }

  return (
    <SafeAreaView style={styles.safe}>
      <StatusBar style="auto" />
      <ScrollView contentContainerStyle={styles.container} keyboardShouldPersistTaps="handled">
        <Text style={styles.h1}>Open Health Sync example</Text>

        <Text style={styles.h2}>Settings</Text>
        <Text style={styles.label}>Server URL</Text>
        <TextInput
          style={styles.input}
          testID="server-url"
          accessibilityLabel="Server URL"
          value={url}
          onChangeText={setUrl}
          autoCapitalize="none"
          autoCorrect={false}
          keyboardType="url"
        />
        <Text style={styles.label}>User token (upload scope)</Text>
        <TextInput
          style={styles.input}
          testID="user-token"
          accessibilityLabel="User token"
          value={tokenInput}
          onChangeText={setTokenInput}
          placeholder={hasToken ? "Saved. Paste a new one to replace it." : "ohs_ut_..."}
          autoCapitalize="none"
          autoCorrect={false}
          secureTextEntry
        />
        <View style={styles.row}>
          <Button title="Save" testID="save" onPress={save} />
          {hasToken ? <Button title="Forget token" testID="forget-token" onPress={forgetToken} /> : null}
        </View>

        <Text style={styles.h2}>Upload</Text>
        <Text style={styles.label}>Heart rate (beats per minute)</Text>
        <TextInput
          style={styles.input}
          testID="heart-rate"
          accessibilityLabel="Heart rate in beats per minute"
          value={bpm}
          onChangeText={setBpm}
          keyboardType="number-pad"
        />
        <View style={styles.row}>
          <Button title="Upload" testID="upload" onPress={upload} disabled={busy || !hasToken} />
          {lastParts ? (
            <Button
              title="Send again"
              testID="send-again"
              onPress={() => void send(lastParts)}
              disabled={busy || !hasToken}
            />
          ) : null}
        </View>
        {busy ? <ActivityIndicator style={styles.spacer} /> : null}

        {rows.map((r) => (
          <View key={r.partNumber} style={styles.card}>
            <Text>Part {r.partNumber}</Text>
            <Text>Type: {r.type}</Text>
            <Text>Day: {r.day}</Text>
            <Text style={styles.strong}>{r.duplicate ? "Duplicate (already stored)" : "Stored"}</Text>
          </View>
        ))}

        {error ? (
          <View style={[styles.card, styles.errorCard]}>
            <Text style={styles.strong}>Upload failed</Text>
            {error.status !== undefined ? <Text>Status: {error.status}</Text> : null}
            {error.title ? <Text>Title: {error.title}</Text> : null}
            {error.requestId ? <Text selectable>Request id: {error.requestId}</Text> : null}
            <Text>{error.message}</Text>
          </View>
        ) : null}
      </ScrollView>
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  safe: { flex: 1 },
  container: { padding: 16, gap: 8 },
  h1: { fontSize: 22, fontWeight: "600", marginBottom: 8 },
  h2: { fontSize: 18, fontWeight: "600", marginTop: 16 },
  label: { fontSize: 14, opacity: 0.7 },
  input: { borderWidth: 1, borderColor: "#999", borderRadius: 6, padding: 8, fontSize: 16 },
  row: { flexDirection: "row", gap: 12, marginTop: 4 },
  spacer: { marginTop: 12 },
  card: { borderWidth: 1, borderColor: "#999", borderRadius: 6, padding: 12, marginTop: 12, gap: 2 },
  errorCard: { borderColor: "#c33" },
  strong: { fontWeight: "600" },
});
