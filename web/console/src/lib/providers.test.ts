import { keysPatch, syncSchedule, type KeyField } from "./providers";

test("keysPatch sends only what changed", () => {
  const fields: KeyField[] = [
    { name: "CLIENT_ID", secret: false, set: true, value: "abc", input: "abc", clear: false },
    { name: "CLIENT_SECRET", secret: true, set: true, value: null, input: "", clear: false },
    { name: "REGION", secret: false, set: false, value: null, input: "", clear: false },
  ];
  expect(keysPatch(fields), "nothing typed: nothing changes").toBeNull();
  const [id, secret, region] = fields as [KeyField, KeyField, KeyField];
  id.input = " new-id ";
  secret.input = "s3cret";
  expect(keysPatch(fields)).toEqual({ CLIENT_ID: "new-id", CLIENT_SECRET: "s3cret" });
  id.input = "abc";
  secret.input = "";
  secret.clear = true;
  region.clear = true;
  expect(keysPatch(fields), "clearing an unset key sends nothing").toEqual({ CLIENT_SECRET: null });
});

test("syncSchedule", () => {
  expect(syncSchedule(null)).toEqual([]);
  const dexcom = {
    pull_every_seconds: 21600,
    pull_every_without_push_seconds: 3600,
    lookback_seconds: 21600,
    max_catch_up_seconds: 604800,
    backfill_seconds: 7776000,
    backfill_chunk_seconds: 604800,
    reconcile_seconds: 172800,
  };
  // Dexcom sends no notifications: only the interval without them applies.
  expect(syncSchedule(dexcom)).toEqual([
    ["Pulled", "Every 1 h"],
    ["Each pull reads again", "The last 6 h, for late or revised data"],
    ["After an outage", "Catches up to 7 d"],
    ["New connections", "Backfilled over 90 d, 7 d per job"],
    ["Daily reconciliation", "The first pull after 00:00 UTC reads the last 2 d"],
  ]);
  expect(syncSchedule(dexcom, true).slice(0, 2)).toEqual([
    ["Pulled while notifications arrive", "Every 6 h"],
    ["Pulled without notifications", "Every 1 h"],
  ]);
  const rows = syncSchedule({ ...dexcom, pull_every_seconds: 86400, pull_every_without_push_seconds: 86400, reconcile_seconds: 0 }, true);
  expect(rows[0]).toEqual(["Pulled", "Every 1 d"]);
  expect(rows.at(-1)).toEqual(["Daily reconciliation", "Off"]);
});
