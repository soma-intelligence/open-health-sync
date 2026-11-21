import { useId } from "react";

import type { UpdateWebhookRequest, Webhook, WebhookEventType } from "../../api/types";
import { EVENT_TYPES } from "./events";

export interface EndpointValues {
  url: string;
  description: string;
  eventTypes: readonly WebhookEventType[];
}

export const EMPTY_VALUES: EndpointValues = { url: "", description: "", eventTypes: [] };

export function valuesOf(hook: Webhook): EndpointValues {
  return { url: hook.url, description: hook.description, eventTypes: hook.event_types };
}

function sameTypes(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((t) => b.includes(t));
}

/** The fields that differ from the endpoint, and only those: what PATCH sends. */
export function changedFields(hook: Webhook, values: EndpointValues): UpdateWebhookRequest {
  const body: UpdateWebhookRequest = {};
  const url = values.url.trim();
  const description = values.description.trim();
  if (url !== hook.url) body.url = url;
  if (description !== hook.description) body.description = description;
  if (!sameTypes(values.eventTypes, hook.event_types)) body.event_types = [...values.eventTypes];
  return body;
}

export interface EndpointFieldsProps {
  values: EndpointValues;
  onChange: (values: EndpointValues) => void;
}

/** URL, description and event types. Controlled, and no form of its own: it sits inside a confirmation or a card form. */
export function EndpointFields({ values, onChange }: EndpointFieldsProps) {
  const id = useId();
  const urlId = `${id}-url`;
  const descId = `${id}-desc`;
  const toggle = (type: WebhookEventType, on: boolean) => {
    const next = EVENT_TYPES.filter((t) => (t === type ? on : values.eventTypes.includes(t)));
    onChange({ ...values, eventTypes: next });
  };
  return (
    <div className="endpoint-fields">
      <div className="field">
        <label htmlFor={urlId}>Endpoint URL</label>
        <input
          id={urlId}
          type="url"
          className="input mono"
          placeholder="https://example.com/hooks/ohs"
          autoComplete="off"
          spellCheck={false}
          value={values.url}
          onChange={(e) => {
            onChange({ ...values, url: e.target.value });
          }}
        />
        <p className="muted">HTTPS. Each delivery is signed; verify it with the signing secret.</p>
      </div>
      <div className="field">
        <label htmlFor={descId}>Description (optional)</label>
        <input
          id={descId}
          type="text"
          className="input"
          placeholder="Research warehouse"
          autoComplete="off"
          value={values.description}
          onChange={(e) => {
            onChange({ ...values, description: e.target.value });
          }}
        />
      </div>
      <fieldset className="event-types">
        <legend>
          Event types <span className="muted">(none checked: every type)</span>
        </legend>
        <div className="type-grid">
          {EVENT_TYPES.map((t) => (
            <label key={t} className="check">
              <input
                type="checkbox"
                value={t}
                checked={values.eventTypes.includes(t)}
                onChange={(e) => {
                  toggle(t, e.target.checked);
                }}
              />
              <span className="mono">{t}</span>
            </label>
          ))}
        </div>
      </fieldset>
    </div>
  );
}
