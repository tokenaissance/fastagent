/**
 * Placement (docs/chat-event-delivery-placement.md §3): a tab that did not
 * start the turn must render the token stream.
 *
 * `content_delta` is live-only — never persisted, so the DB tail cannot carry
 * it — and the subscribe path used to drop it on the server, on the assumption
 * that the only subscriber is the tab that owns the POST. A second browser
 * watching the same session is the counterexample: it has no POST to render
 * from, so before this it showed nothing until the answer appeared whole.
 */
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, act, waitFor } from "@testing-library/react";

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), prefetch: vi.fn() }),
  usePathname: () => "/agents/agent-test/chat/session-1/",
  useSearchParams: () => ({ get: () => null }),
  useParams: () => ({ id: "agent-test" }),
}));

vi.mock("next/link", () => ({
  default: ({ children }: { children: React.ReactNode }) => <span>{children}</span>,
}));

// The assertions are about streaming text, not markdown layout.
vi.mock("@/components/chat-markdown", () => ({
  ChatMarkdown: ({ text }: { text: string }) => <span>{text}</span>,
}));

const api = vi.hoisted(() => ({
  getChatHistoryWithCursor: vi.fn(),
  getChatSessions: vi.fn(),
  getChatTodo: vi.fn(),
  getMe: vi.fn(),
  getSkills: vi.fn(),
  listAgentFiles: vi.fn(),
  listProjects: vi.fn(),
  getAgent: vi.fn(),
  getChangedFiles: vi.fn(),
  getAgentKnowledgeFile: vi.fn(),
  getScopePreview: vi.fn(),
  getScopePreviewLogs: vi.fn(),
  sendChatStream: vi.fn(),
  steerChat: vi.fn(),
  cancelQueuedTurn: vi.fn(),
  uploadAgentFiles: vi.fn(),
  renameChatSession: vi.fn(),
  revealAgentWorkspace: vi.fn(),
  fileUrl: (p: string) => `/api/files/${p}`,
}));

vi.mock("@/lib/api", () => api);

import { ChatScreen } from "@/components/chat-screen";

type FakeEventSource = { url: string; emit: (payload: unknown) => void; close: () => void };
let fakeEventSources: FakeEventSource[] = [];

function installFakeEventSource() {
  fakeEventSources = [];
  class StubEventSource {
    url: string;
    onmessage: ((e: { data: string }) => void) | null = null;
    onerror: (() => void) | null = null;
    constructor(url: string) {
      this.url = url;
      fakeEventSources.push({
        url,
        emit: (payload: unknown) => this.onmessage?.({ data: JSON.stringify(payload) }),
        close: () => {},
      });
    }
    close() {}
  }
  vi.stubGlobal("EventSource", StubEventSource);
}

async function openSubscription() {
  render(<ChatScreen />);
  return waitFor(() => {
    expect(fakeEventSources.length).toBeGreaterThan(0);
    return fakeEventSources[0];
  });
}

describe("chat/subscribe renders the token stream (placement §3)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    installFakeEventSource();
    api.getChatHistoryWithCursor.mockResolvedValue({ history: [], latestEventSeq: 5 });
    api.getChatSessions.mockResolvedValue([]);
    api.getChatTodo.mockResolvedValue({ items: [], raw: "" });
    api.getMe.mockResolvedValue({});
    api.getSkills.mockResolvedValue([]);
    api.listAgentFiles.mockResolvedValue([]);
    api.listProjects.mockResolvedValue([]);
    api.getAgent.mockResolvedValue({ id: "agent-test", name: "agent-test" });
    api.getChangedFiles.mockResolvedValue([]);
    api.getAgentKnowledgeFile.mockResolvedValue(null);
    api.getScopePreview.mockResolvedValue(null);
    api.getScopePreviewLogs.mockResolvedValue([]);
    api.sendChatStream.mockResolvedValue(undefined);
    api.steerChat.mockResolvedValue(undefined);
    api.cancelQueuedTurn.mockResolvedValue("canceled");
    api.uploadAgentFiles.mockResolvedValue([]);
  });

  it("streams a turn this tab did not start, token by token", async () => {
    const es = await openSubscription();

    await act(async () => {
      es.emit({ seq: -1, type: "content_delta", data: { delta: "Hel" } });
    });
    // Visible mid-turn: this is the whole point — the tab has no POST stream.
    expect(screen.getByText("Hel")).toBeInTheDocument();

    await act(async () => {
      es.emit({ seq: -1, type: "content_delta", data: { delta: "lo" } });
    });
    expect(screen.getByText("Hello")).toBeInTheDocument();
  });

  it("seals a mid-round join by replacing the suffix with the round's full text", async () => {
    const es = await openSubscription();

    // A subscription that connects mid-turn cannot replay the deltas that
    // already went by (they were never persisted), so what it accumulates is a
    // suffix of the round. The trailing `content` carries the whole round —
    // appending it would print the suffix twice.
    await act(async () => {
      es.emit({ seq: -1, type: "content_delta", data: { delta: "orld" } });
    });
    expect(screen.getByText("orld")).toBeInTheDocument();

    await act(async () => {
      es.emit({ seq: 6, type: "content", data: { content: "Hello world" } });
    });
    expect(screen.getByText("Hello world")).toBeInTheDocument();
    expect(screen.queryByText("orldHello world")).not.toBeInTheDocument();
  });
});
