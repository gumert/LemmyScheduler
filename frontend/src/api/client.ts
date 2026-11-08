import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";

import { PingService } from "../gen/lemmyscheduler/v1/ping_pb";

// NEXT_PUBLIC_* values are inlined at build time, so a production build needs
// this set when `next build` runs. The default matches the local backend.
const API_URL = process.env.NEXT_PUBLIC_API_URL ?? "http://localhost:8080";

// This is the only module that builds API URLs. Components import the typed
// clients below and never construct URLs themselves.
const transport = createConnectTransport({ baseUrl: API_URL });

export const pingClient = createClient(PingService, transport);
