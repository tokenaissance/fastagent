/**
 * Placement (docs/chat-event-delivery-placement.md §3): the affinity key is the
 * `sessionId` query parameter.
 *
 * The ingress hashes on `$arg_sessionId`, so the id has to be in the URL — and
 * for /chat/subscribe it already is. The stream POST is the one call that keeps
 * it in the body, which the ingress cannot see. Adding it to the query costs one
 * parameter and needs no new header (EventSource cannot set one anyway) and no
 * proxy change.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { sendChatStream } from "@/lib/api";

describe("the stream POST carries the affinity key", () => {
  let calls: { url: string; body: string }[] = [];

  beforeEach(() => {
    calls = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init?: RequestInit) => {
        calls.push({ url: String(url), body: String(init?.body ?? "") });
        // A well-formed frame: the parser rejects anything it cannot JSON-parse,
        // and this test is about the URL the request went to, not the payload.
        return new Response('data: {"seq":1,"type":"done"}\n\n', {
          status: 200,
          headers: { "Content-Type": "text/event-stream" },
        });
      }),
    );
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("puts sessionId in the query as well as the body", async () => {
    await sendChatStream("agent-1", "session-42", "hi", () => {});

    expect(calls).toHaveLength(1);
    expect(calls[0].url).toContain("/api/chat/stream");
    expect(calls[0].url).toContain("sessionId=session-42");
    // The body keeps it too: the server reads the turn's identity from there,
    // and the two must never disagree.
    expect(JSON.parse(calls[0].body).sessionId).toBe("session-42");
  });
});
