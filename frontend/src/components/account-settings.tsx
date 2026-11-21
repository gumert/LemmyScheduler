"use client";

import { useEffect, useState } from "react";

import { lemmyAccountClient } from "../api/client";
import { describeAccountError } from "../errors";
import type { AccountStatus } from "../gen/lemmyscheduler/v1/lemmy_account_pb";
import { formatScheduledAt } from "../time";
import { Field } from "./field";

const REQUEST_TIMEOUT_MS = 30_000;

// The password is write-only: the backend never returns it, so the field is
// never prefilled and is cleared after every attempt.
export function AccountSettings() {
  const [status, setStatus] = useState<AccountStatus | undefined>();
  const [loadError, setLoadError] = useState("");
  const [instanceUrl, setInstanceUrl] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    let cancelled = false;
    lemmyAccountClient
      .getAccountStatus({}, { timeoutMs: REQUEST_TIMEOUT_MS })
      .then((res) => {
        if (cancelled || !res.status) {
          return;
        }
        setStatus(res.status);
        setInstanceUrl(res.status.instanceUrl);
        setUsername(res.status.username);
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setLoadError(describeAccountError(err));
        }
      });
    return () => {
      cancelled = true;
    };
  }, []);

  async function save() {
    setBusy(true);
    setError("");
    setSaved(false);
    try {
      const res = await lemmyAccountClient.setAccount(
        { instanceUrl, username, password },
        { timeoutMs: REQUEST_TIMEOUT_MS },
      );
      setStatus(res.status);
      setSaved(true);
    } catch (err) {
      setError(describeAccountError(err));
    } finally {
      setPassword("");
      setBusy(false);
    }
  }

  const canSave = !busy && instanceUrl.trim() !== "" && username.trim() !== "" && password !== "";

  return (
    <section>
      {loadError && (
        <p className="state state-error" role="alert">
          {loadError}
        </p>
      )}
      <StatusSummary status={status} />
      <form
        className="post-form"
        onSubmit={(event) => {
          event.preventDefault();
          if (canSave) {
            void save();
          }
        }}
      >
        <h2>Lemmy account</h2>
        <p className="muted">
          Needs a Lemmy 0.19+ instance and an account without two-factor authentication. The password is stored in plain
          text in this app&apos;s database and is never shown again. Saving checks the login first.
        </p>
        {error && (
          <p className="state state-error" role="alert">
            {error}
          </p>
        )}
        {saved && (
          <p className="state state-info" role="status">
            Account saved.
          </p>
        )}
        <fieldset disabled={busy} className="fields">
          <Field label="Instance URL">
            <input
              value={instanceUrl}
              onChange={(e) => setInstanceUrl(e.target.value)}
              placeholder="https://lemmy.example"
              autoComplete="off"
            />
          </Field>
          <Field label="Username">
            <input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" />
          </Field>
          <Field label="Password (required every time you save)">
            <input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="new-password"
            />
          </Field>
          <button type="submit" className="button primary" disabled={!canSave}>
            {busy ? "Checking…" : "Save account"}
          </button>
        </fieldset>
      </form>
    </section>
  );
}

function StatusSummary({ status }: { status: AccountStatus | undefined }) {
  if (!status) {
    return null;
  }
  if (!status.configured) {
    return <p className="state state-info">No Lemmy account is configured yet.</p>;
  }
  const when = status.lastLoginAt ? formatScheduledAt(status.lastLoginAt) : "";
  return (
    <p className={status.lastLoginOk ? "state state-info" : "state state-error"} role="status">
      {status.username} on {status.instanceUrl}.{" "}
      {status.lastLoginOk ? `Last login succeeded ${when}.` : `Last login failed ${when}: ${status.lastLoginError}`}
    </p>
  );
}
