/**
 * Parity finding D4 (decided 2026-09-15): the `chat/subscribe` path must render
 * a turn it did not start WHILE that turn runs.
 *
 * `tool_call` / `tool_result` used to have no case in the subscription handler:
 * the rows appeared only after `done` triggered the history reload. A user
 * watching another tab's turn therefore stared at "Executing..." with no record
 * of what was executing — and a turn that never wrote a closing message showed
 * nothing at all. cloud is the reference here (its reducer renders both).
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

// Streamdown pulls a full markdown pipeline; the assertions here are about tool
// rows, so render assistant text as plain text.
vi.mock("@/components/chat-markdown", () => ({
  ChatMarkdown: ({ text }: { text: string }) => <span>{text}</span>,
}));

// vi.hoisted: the mock factory below is hoisted above these declarations, so a
// plain `const` would still be in the temporal dead zone when it runs.
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

describe("chat/subscribe renders tool rows (D4)", () => {
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

  it("shows a running tool row before `done`, then its result", async () => {
    render(<ChatScreen />);

    const es = await waitFor(() => {
      expect(fakeEventSources.length).toBeGreaterThan(0);
      return fakeEventSources[0];
    });

    // Control: a content event on this very connection DOES render, so a
    // missing tool row below cannot be blamed on the connection or the stub.
    await act(async () => {
      es.emit({ seq: 6, type: "content", data: { content: "a reply from another tab" } });
    });
    expect(screen.getByText("a reply from another tab")).toBeInTheDocument();

    await act(async () => {
      es.emit({
        seq: 7,
        type: "tool_call",
        data: { id: "t1", name: "exec", arguments: '{"command":"ls"}' },
      });
    });

    // Running, and visible: the row names the tool while it is still in flight.
    expect(screen.getByText("exec")).toBeInTheDocument();
    expect(screen.getByText("Running tools (0/1)...")).toBeInTheDocument();

    await act(async () => {
      es.emit({ seq: 8, type: "tool_result", data: { id: "t1", result: "ok" } });
    });

    expect(screen.getByText("Executed 1 tool")).toBeInTheDocument();
  });

  it("canonicalises from history on `done` after painting tool rows", async () => {
    // The other half of D4: rows rendered live are a preview — the durable
    // record (padded tool results, the final round's text) is the history read,
    // so `done` owes one whenever this connection painted rows. A tool-only
    // turn has no content bubble, so the old condition (a transient bubble
    // exists) missed exactly this case. The assertion is behavioural rather
    // than a call count: this text exists ONLY in the second history read.
    render(<ChatScreen />);
    const es = await waitFor(() => {
      expect(fakeEventSources.length).toBeGreaterThan(0);
      return fakeEventSources[0];
    });

    await act(async () => {
      es.emit({
        seq: 6,
        type: "tool_call",
        data: { id: "t1", name: "exec", arguments: '{"command":"ls"}' },
      });
    });
    expect(screen.getByText("Running tools (0/1)...")).toBeInTheDocument();

    // The turn ends; the canonical transcript differs from what streamed.
    api.getChatHistoryWithCursor.mockResolvedValue({
      history: [{ role: "assistant", content: "canonical answer from history" }],
      latestEventSeq: 8,
    });
    await act(async () => {
      es.emit({ seq: 7, type: "done" });
    });

    expect(await screen.findByText("canonical answer from history")).toBeInTheDocument();
    expect(screen.queryByText("Running tools (0/1)...")).not.toBeInTheDocument();
  });
});
