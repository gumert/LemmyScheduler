import { useEffect, useState } from "react";

import { lemmyAccountClient } from "./api/client";
import { isNoAccountError } from "./errors";
import type { Community } from "./gen/lemmyscheduler/v1/lemmy_account_pb";

const SUGGESTION_TIMEOUT_MS = 5_000;

export function communitySuggestionLabel(community: Pick<Community, "name" | "title">): string {
  return community.title ? `${community.name} — ${community.title}` : community.name;
}

// Loads the followed communities once. Suggestions are optional: any failure
// leaves the list empty and the form exactly as it is without them. Only a
// missing account is reported, so the form can link to Settings.
export function useCommunitySuggestions(): { communities: Community[]; noAccount: boolean } {
  const [state, setState] = useState<{ communities: Community[]; noAccount: boolean }>({
    communities: [],
    noAccount: false,
  });

  useEffect(() => {
    let cancelled = false;
    lemmyAccountClient
      .listCommunities({}, { timeoutMs: SUGGESTION_TIMEOUT_MS })
      .then((res) => {
        if (!cancelled) {
          setState({ communities: res.communities, noAccount: false });
        }
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setState({ communities: [], noAccount: isNoAccountError(err) });
        }
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return state;
}
