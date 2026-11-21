import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import { useParams } from "react-router";

import { ApiError } from "../../api/client";
import { downloadFile } from "../../api/download";
import type { Setting, SettingsList } from "../../api/types";
import { useInspect, type InspectorEntry } from "../../inspector/InspectorProvider";
import { useApi, useCanWrite, useIsAdmin } from "../../session/SessionProvider";
import { Button } from "../../ui/Button";
import { ErrorPanel } from "../../ui/ErrorPanel";
import { useToast } from "../../ui/Toast";
import { SETTINGS_EXPORT_PATH, SETTINGS_KEY, SETTINGS_PATH, useApplySettings, useProcessesQuery, useSettingsQuery } from "./api";
import { useDraft } from "./DraftProvider";
import { SourceBadge } from "./FieldMeta";
import { diffRows, fieldState, prunePending, restartCount, retentionWarnings, sectionSlug, toPatch, type ApplyOutcome } from "./model";
import { ProcessesBanner } from "./ProcessesBanner";
import { RestartNotice } from "./RestartNotice";
import { ReviewDialog } from "./ReviewDialog";
import { SaveBar } from "./SaveBar";
import { controlId, fieldId } from "./SettingField";
import { SettingsSection } from "./SettingsSection";
import { SettingsSubnav } from "./SettingsSubnav";

import "./settings.css";
import { Skeleton } from "../../ui/Skeleton";

const MOVED_NOTE = "The settings changed since you opened this review. Here are the new values.";

interface Review {
  /** The list the diff is built on; its revision goes in If-Match. */
  base: SettingsList;
  note: string | null;
  error: unknown;
}

function groupBySection(settings: readonly Setting[]): [string, Setting[]][] {
  const out = new Map<string, Setting[]>();
  for (const s of settings) {
    const list = out.get(s.section);
    if (list) list.push(s);
    else out.set(s.section, [s]);
  }
  return [...out.entries()];
}

function droppedNote(names: readonly string[]): string {
  return names.length === 0 ? "" : ` Taken out of your changes: ${names.join(", ")}.`;
}

function ExportButtons() {
  const api = useApi();
  const toast = useToast();
  const copy = async () => {
    try {
      const { data } = await api.request<string>("GET", SETTINGS_EXPORT_PATH, { as: "text" });
      await navigator.clipboard.writeText(data);
      toast("Copied as .env.");
    } catch (e) {
      toast(e instanceof ApiError ? e.detail || e.message : "Copy failed.", "bad");
    }
  };
  const save = async () => {
    try {
      await downloadFile(api, SETTINGS_EXPORT_PATH, "ohs.env");
    } catch (e) {
      toast(e instanceof ApiError ? e.detail || e.message : "Export failed.", "bad");
    }
  };
  return (
    <>
      <Button
        onClick={() => {
          void copy();
        }}
      >
        Copy as .env
      </Button>
      <Button
        onClick={() => {
          void save();
        }}
      >
        Export
      </Button>
    </>
  );
}

function AdminSettings() {
  const { section: sectionParam, name: nameParam } = useParams();
  const canWrite = useCanWrite();
  const toast = useToast();
  const queryClient = useQueryClient();
  const query = useSettingsQuery();
  const processes = useProcessesQuery();
  const apply = useApplySettings();
  const draft = useDraft();
  const { data, error } = query;

  const [serverErrors, setServerErrors] = useState<Readonly<Record<string, string>>>({});
  const [review, setReview] = useState<Review | null>(null);
  const [restart, setRestart] = useState<readonly string[] | null>(null);

  const settings = useMemo(() => data?.data ?? [], [data]);
  const groups = useMemo(() => groupBySection(settings), [settings]);
  const sections = useMemo(() => groups.map(([section]) => section), [groups]);
  const rows = useMemo(() => diffRows(settings, draft.pending), [settings, draft.pending]);
  const blocked = settings.some((s) => fieldState(s, draft.pending).inputError !== null);
  const changedSections = useMemo(() => {
    const out = new Set<string>();
    for (const s of settings) if (Object.hasOwn(draft.pending, s.name)) out.add(s.section);
    return out;
  }, [settings, draft.pending]);
  const focused = nameParam ? (settings.find((s) => s.name === nameParam) ?? null) : null;

  const entry = useMemo<InspectorEntry | null>(() => {
    const request = { method: "GET" as const, path: SETTINGS_PATH };
    if (focused) return { kind: "setting", title: focused.name, id: focused.name, request, response: focused };
    if (data !== undefined) return { kind: "settings", title: "Settings", request, response: data };
    return error instanceof ApiError ? { kind: "settings", title: "Settings", request, response: undefined, error } : null;
  }, [focused, data, error]);
  useInspect(entry);

  // An address naming a section or a setting scrolls to it; a setting also takes the focus.
  const loaded = data !== undefined;
  useEffect(() => {
    if (!loaded) return;
    const target = nameParam
      ? document.getElementById(fieldId(nameParam))
      : sectionParam
        ? document.getElementById(`section-${sectionParam}`)
        : null;
    if (!target) return;
    if (typeof target.scrollIntoView === "function") target.scrollIntoView({ block: "start" });
    if (nameParam) {
      const control = document.getElementById(controlId(nameParam)) ?? target.querySelector<HTMLElement>("input, select, button");
      control?.focus({ preventScroll: true });
    }
  }, [loaded, sectionParam, nameParam]);

  const change = (name: string, v: string | null | undefined) => {
    setServerErrors((all) => (Object.hasOwn(all, name) ? Object.fromEntries(Object.entries(all).filter(([n]) => n !== name)) : all));
    const s = settings.find((x) => x.name === name);
    // Back to what is in effect, or an emptied secret: no change.
    if (v === undefined || (s && v !== null && (s.secret ? v === "" : v === s.value))) {
      draft.drop([name]);
      return;
    }
    draft.set(name, v);
  };

  const reload = async (): Promise<SettingsList | null> => {
    const res = await query.refetch();
    // A failed refetch keeps the old data: never rebuild a review on it.
    if (res.isError) return null;
    return res.data ?? null;
  };

  const runApply = async () => {
    if (!review) return;
    let outcome: ApplyOutcome;
    try {
      outcome = await apply.mutateAsync({ revision: review.base.revision, patch: toPatch(draft.pending) });
    } catch (e) {
      setReview((r) => (r ? { ...r, error: e } : r));
      return;
    } finally {
      // The body may hold a typed secret: no copy stays in the mutation cache.
      apply.reset();
    }
    switch (outcome.kind) {
      case "ok":
        draft.discard();
        setServerErrors({});
        setReview(null);
        setRestart(outcome.restart.length > 0 ? outcome.restart : null);
        toast("Settings saved.");
        void queryClient.invalidateQueries({ queryKey: SETTINGS_KEY });
        return;
      case "moved": {
        const fresh = await reload();
        if (!fresh) {
          setReview((r) => (r ? { ...r, error: new Error("The settings changed and could not be read again. Try again.") } : r));
          return;
        }
        const pruned = prunePending(fresh.data, draft.pending);
        draft.drop(pruned.dropped);
        setReview({ base: fresh, note: MOVED_NOTE + droppedNote(pruned.dropped), error: null });
        return;
      }
      case "conflict": {
        const names = outcome.names;
        draft.drop(names);
        const fresh = await reload();
        const message = `These settings are not the console's to change: ${names.join(", ")}. Nothing was saved. They are out of your changes now.`;
        setReview((r) => (r ? { base: fresh ?? r.base, note: null, error: new Error(message) } : r));
        return;
      }
      case "invalid":
        if (!Object.keys(outcome.errors).some((name) => settings.some((x) => x.name === name))) {
          // Nothing on the page to mark: keep the review and the API's own message.
          setReview((r) => (r ? { ...r, error: outcome.error } : r));
          return;
        }
        setServerErrors(outcome.errors);
        setReview(null);
        toast("Nothing was saved. See the marked fields.", "bad");
        return;
    }
  };

  const reviewRows = review ? diffRows(review.base.data, draft.pending) : [];
  const reviewWarnings = review ? retentionWarnings(review.base.data, draft.pending) : [];

  return (
    <div className="page set-page">
      <header className="page-head">
        <div>
          <h1 className="page-title">Settings</h1>
          <p className="page-sub">Instance settings, saved in the database. The environment wins over the console.</p>
        </div>
        <div className="page-actions">{data ? <ExportButtons /> : null}</div>
      </header>
      {restart ? (
        <RestartNotice
          names={restart}
          onDismiss={() => {
            setRestart(null);
          }}
        />
      ) : null}
      <ProcessesBanner processes={processes.data?.data ?? []} />
      {query.isPending ? <Skeleton kind="lines" count={6} /> : null}
      {error !== null && data === undefined ? <ErrorPanel error={error} retry /> : null}
      {data ? (
        <div className="set-layout">
          <SettingsSubnav sections={sections} current={sectionParam ?? (focused ? sectionSlug(focused.section) : null)} changed={changedSections} />
          <div className="set-main">
            <p className="set-prec">
              <SourceBadge source="env" />
              <span className="set-gt" aria-hidden="true">
                &gt;
              </span>
              <SourceBadge source="console" />
              <span className="set-gt" aria-hidden="true">
                &gt;
              </span>
              <SourceBadge source="default" />
              <span className="set-prec-r">
                <span className="set-badge set-apply restart">Restart</span> applies when a process next starts
              </span>
            </p>
            {groups.map(([section, list]) => (
              <SettingsSection
                key={`${section} ${String(draft.epoch)}`}
                section={section}
                settings={list}
                pending={draft.pending}
                canWrite={canWrite}
                errors={serverErrors}
                focusedName={nameParam ?? null}
                onChange={change}
              />
            ))}
          </div>
        </div>
      ) : null}
      {canWrite && rows.length > 0 ? (
        <SaveBar
          count={rows.length}
          restarts={restartCount(rows)}
          blocked={blocked}
          onDiscard={() => {
            draft.discard();
            setServerErrors({});
          }}
          onReview={() => {
            if (data) setReview({ base: data, note: null, error: null });
          }}
        />
      ) : null}
      {review ? (
        <ReviewDialog
          rows={reviewRows}
          warnings={reviewWarnings}
          note={review.note}
          error={review.error}
          busy={apply.isPending}
          onApply={() => {
            void runApply();
          }}
          onClose={() => {
            setReview(null);
          }}
        />
      ) : null}
    </div>
  );
}

/** Settings of the instance: every setting by section, a draft, a review, one PATCH. */
export function SettingsPage() {
  const admin = useIsAdmin();
  if (!admin) {
    return (
      <div className="page">
        <header className="page-head">
          <div>
            <h1 className="page-title">Settings</h1>
          </div>
        </header>
        <p>Settings belong to the instance. Sign in with a key of an instance admin tenant.</p>
      </div>
    );
  }
  return <AdminSettings />;
}
