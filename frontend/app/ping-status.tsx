"use client";

import { useEffect, useState } from "react";

import { pingClient } from "../src/api/client";

// Keeps the page from hanging when the backend is down or unreachable.
const PING_TIMEOUT_MS = 5000;

type PingState =
  | { status: "loading" }
  | { status: "ok"; message: string }
  | { status: "error"; error: string };

export function PingStatus() {
  const [state, setState] = useState<PingState>({ status: "loading" });

  useEffect(() => {
    const controller = new AbortController();

    pingClient
      .ping({}, { timeoutMs: PING_TIMEOUT_MS, signal: controller.signal })
      .then((res) => setState({ status: "ok", message: res.message }))
      .catch((err: unknown) => {
        if (controller.signal.aborted) {
          return;
        }
        setState({
          status: "error",
          error: err instanceof Error ? err.message : String(err),
        });
      });

    return () => controller.abort();
  }, []);

  switch (state.status) {
    case "loading":
      return <p className="muted">Contacting backend…</p>;
    case "ok":
      return <p>Backend says: {state.message}</p>;
    case "error":
      return (
        <p role="alert">Backend unreachable: {state.error}</p>
      );
  }
}
