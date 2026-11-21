import type { Setting } from "../../api/types";
import { sectionSlug, type Pending } from "./model";
import { SettingField } from "./SettingField";

export function sectionId(section: string): string {
  return `section-${sectionSlug(section)}`;
}

export interface SettingsSectionProps {
  section: string;
  settings: readonly Setting[];
  pending: Pending;
  canWrite: boolean;
  errors: Readonly<Record<string, string>>;
  focusedName: string | null;
  onChange: (name: string, v: string | null | undefined) => void;
}

export function SettingsSection({ section, settings, pending, canWrite, errors, focusedName, onChange }: SettingsSectionProps) {
  const headingId = `${sectionId(section)}-h`;
  return (
    <section id={sectionId(section)} className="set-section" aria-labelledby={headingId} data-slug={sectionSlug(section)}>
      <h2 id={headingId} className="set-section-h">
        {section}
      </h2>
      <div className="set-fields">
        {settings.map((s) => (
          <SettingField
            key={s.name}
            setting={s}
            pending={pending}
            canWrite={canWrite}
            serverError={errors[s.name] ?? null}
            focused={focusedName === s.name}
            onChange={onChange}
          />
        ))}
      </div>
    </section>
  );
}
