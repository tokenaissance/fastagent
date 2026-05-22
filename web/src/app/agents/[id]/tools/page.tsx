"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Check, MessageSquare, Wrench, Info } from "lucide-react";
import {
  getAgent,
  listAgentRegisteredTools,
  updateAgent,
  type AgentRegisteredTool,
} from "@/lib/api";
import { useAgentIdFromURL } from "@/hooks/use-agent-id";
import { useAgentName } from "@/hooks/use-agent-name";
import { cn } from "@/lib/utils";

// Per-agent Tools page.
//
//   Prompt Mode      — controls the FRAMEWORK section of the system prompt
//                      (fastclaw boilerplate). Persona content
//                      (SOUL.md / IDENTITY.md / USER.md / MEMORY.md) is
//                      always injected on top regardless of mode; edit it
//                      in the Customize tab.
//   Tool Allowlist   — restricts which registered tools the LLM can call.
//                      Empty list = no filter (sees every registered
//                      tool, current default behavior).
//
// The allowlist UX is a clickable grid of pills sourced from the live
// per-agent registry (/api/agents/{id}/tools/registered). Click toggles
// in / out of the allowlist. We also surface a free-text "Additional"
// field for tool names the live registry doesn't know about yet —
// useful when an operator wants to pre-authorize an MCP-provided tool
// that will appear after the next reload.

const SOURCE_LABEL: Record<string, string> = {
  builtin: "Built-in",
  mcp: "MCP",
  plugin: "Plugin",
};

export default function AgentToolsPage() {
  const agentId = useAgentIdFromURL();
  const agentName = useAgentName(agentId);

  // "" means "no override saved" — runtime falls back to "agent".
  const [promptMode, setPromptMode] = useState<"" | "agent" | "chatbot" | "minimal">("");
  // Canonical allowlist as a set so toggling is O(1).
  const [allowed, setAllowed] = useState<Set<string>>(new Set());
  // Tools known to the live registry. null until first fetch
  // resolves; empty array means the agent has 0 tools registered.
  const [registered, setRegistered] = useState<AgentRegisteredTool[] | null>(null);
  // registryUnavailable: backend returned 404 because the agent isn't
  // attached in this UserSpace yet. Allowlist still works (we'll save
  // the names operator typed) but the picker can't render.
  const [registryUnavailable, setRegistryUnavailable] = useState(false);
  // Names in the saved allowlist that are NOT in the live registry —
  // shown in the "Additional" field so the operator can keep them
  // without surprise data loss when MCP isn't connected yet.
  const [extraText, setExtraText] = useState("");

  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);

  const fetchAll = useCallback(async () => {
    if (!agentId) return;
    setLoading(true);
    try {
      const [agentRec, tools] = await Promise.all([
        getAgent(agentId).catch(() => null),
        listAgentRegisteredTools(agentId).catch(() => null),
      ]);
      const pm = agentRec?.promptMode || "";
      if (pm === "agent" || pm === "chatbot" || pm === "minimal") {
        setPromptMode(pm);
      } else {
        setPromptMode("");
      }
      const allow = new Set(agentRec?.toolAllowlist || []);
      setAllowed(allow);
      setRegistered(tools);
      setRegistryUnavailable(tools === null);
      // Compute extras: allowlist names that aren't in the live registry.
      const registryNames = new Set((tools || []).map((t) => t.name));
      const extras = (agentRec?.toolAllowlist || []).filter(
        (n) => !registryNames.has(n),
      );
      setExtraText(extras.join(", "));
    } finally {
      setLoading(false);
    }
  }, [agentId]);

  useEffect(() => {
    fetchAll();
  }, [fetchAll]);

  const flashSaved = () => {
    setSaved(true);
    setTimeout(() => setSaved(false), 2000);
  };

  // Persist a merged allowlist built from the live registry checkboxes
  // + the free-text "Additional" tools. Centralized so any change path
  // (toggle / clear / extras blur) routes through one save.
  const persistAllowlist = useCallback(
    async (nextAllowed: Set<string>, nextExtras: string[]) => {
      // Names in `nextAllowed` MAY include extras that are already there
      // (when the user toggled an "additional" tool back on via checkbox
      // — though we don't expose that UI). Use a final set to dedupe.
      const merged = new Set<string>();
      nextAllowed.forEach((n) => merged.add(n));
      nextExtras.forEach((n) => merged.add(n));
      const arr = Array.from(merged);
      setSaving(true);
      try {
        await updateAgent(agentId, { toolAllowlist: arr });
        flashSaved();
      } finally {
        setSaving(false);
      }
    },
    [agentId],
  );

  const handleToggle = (name: string) => {
    const next = new Set(allowed);
    if (next.has(name)) {
      next.delete(name);
    } else {
      next.add(name);
    }
    setAllowed(next);
    const extras = extraText
      .split(",")
      .map((t) => t.trim())
      .filter((t) => t.length > 0);
    void persistAllowlist(next, extras);
  };

  const handleClearAll = () => {
    setAllowed(new Set());
    setExtraText("");
    void persistAllowlist(new Set(), []);
  };

  // PromptMode change: optimistic update.
  const handlePromptModeChange = async (next: "" | "agent" | "chatbot" | "minimal") => {
    const prev = promptMode;
    setPromptMode(next);
    setSaving(true);
    try {
      await updateAgent(agentId, { promptMode: next });
      flashSaved();
    } catch {
      setPromptMode(prev);
    } finally {
      setSaving(false);
    }
  };

  const handleExtrasBlur = () => {
    const extras = extraText
      .split(",")
      .map((t) => t.trim())
      .filter((t) => t.length > 0);
    setExtraText(extras.join(", "));
    void persistAllowlist(allowed, extras);
  };

  // Build display rows: live-registry tools first, then any "extras"
  // (allowed names not in the registry). Stable order from backend.
  const grouped = useMemo(() => {
    if (!registered) return null;
    const order: Record<string, AgentRegisteredTool[]> = {
      builtin: [],
      mcp: [],
      plugin: [],
      other: [],
    };
    for (const t of registered) {
      const k = t.source in order ? t.source : "other";
      order[k].push(t);
    }
    return order;
  }, [registered]);

  const totalAllowed = allowed.size +
    (extraText.trim() === ""
      ? 0
      : extraText.split(",").filter((s) => s.trim().length > 0).length);
  const restricted = totalAllowed > 0;

  if (loading) {
    return (
      <div className="p-6 space-y-6 max-w-5xl mx-auto">
        <Skeleton className="h-10 w-48" />
        <Skeleton className="h-32 w-full" />
        <Skeleton className="h-48 w-full" />
      </div>
    );
  }

  return (
    <div className="p-6 space-y-6 max-w-5xl mx-auto">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-2xl font-semibold tracking-tight">Tools</h2>
          <p className="text-sm text-muted-foreground mt-1">
            What the LLM sees and how it thinks for{" "}
            <strong>{agentName || "this agent"}</strong>. Persona content
            lives in the Customize tab — it&apos;s always injected
            regardless of the framework mode.
          </p>
        </div>
        <div className="flex items-center gap-2">
          {saved && (
            <span className="inline-flex items-center gap-1.5 text-xs text-emerald-600 dark:text-emerald-400">
              <Check className="h-3.5 w-3.5" /> Saved
            </span>
          )}
        </div>
      </div>

      {/* Prompt Mode */}
      <div className="rounded-lg border border-border bg-card p-5">
        <div className="flex items-center justify-between gap-2 mb-3">
          <div className="flex items-center gap-2">
            <MessageSquare className="h-4 w-4 text-primary" />
            <h3 className="font-medium">Prompt mode</h3>
            {promptMode === "" || promptMode === "agent" ? (
              <Badge variant="outline" className="text-[10px]">
                Default
              </Badge>
            ) : (
              <Badge className="bg-primary/10 text-primary hover:bg-primary/10 text-[10px]">
                Override
              </Badge>
            )}
          </div>
          {promptMode !== "" && (
            <Button
              variant="ghost"
              size="sm"
              className="h-7 text-xs"
              onClick={() => handlePromptModeChange("")}
              disabled={saving}
            >
              Clear override
            </Button>
          )}
        </div>
        <Select
          value={promptMode || "agent"}
          onValueChange={(v: string | null) => {
            if (v === "agent" || v === "chatbot" || v === "minimal") {
              handlePromptModeChange(v);
            }
          }}
          disabled={saving}
        >
          <SelectTrigger className="text-sm max-w-md">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="agent">Agent — full framework prompt (default)</SelectItem>
            <SelectItem value="chatbot">Chatbot — slim, persona-driven</SelectItem>
            <SelectItem value="minimal">Minimal — bootstrap files only</SelectItem>
          </SelectContent>
        </Select>
        <p className="text-xs text-muted-foreground mt-2">
          <strong>Agent</strong> emits the full framework prompt (task
          delegation, tool-use discipline, workspace self-update,
          scheduling). <strong>Chatbot</strong> drops those agent-loop
          sections so persona files (SOUL.md / IDENTITY.md) shape voice
          directly — recommended for companion / role-play /
          customer-support bots. <strong>Minimal</strong> emits only the
          date + bootstrap files; you take full responsibility for what
          the LLM sees.
        </p>
      </div>

      {/* Tool Allowlist */}
      <div className="rounded-lg border border-border bg-card p-5">
        <div className="flex items-center justify-between gap-2 mb-3">
          <div className="flex items-center gap-2">
            <Wrench className="h-4 w-4 text-primary" />
            <h3 className="font-medium">Tool allowlist</h3>
            {!restricted ? (
              <Badge variant="outline" className="text-[10px]">
                All tools
              </Badge>
            ) : (
              <Badge className="bg-primary/10 text-primary hover:bg-primary/10 text-[10px]">
                {totalAllowed} allowed
              </Badge>
            )}
          </div>
          {restricted && (
            <Button
              variant="ghost"
              size="sm"
              className="h-7 text-xs"
              onClick={handleClearAll}
              disabled={saving}
            >
              Clear all
            </Button>
          )}
        </div>

        <p className="text-xs text-muted-foreground mb-4">
          Click a tool to toggle it. With nothing selected the LLM sees
          <strong> every</strong> registered tool (legacy default). Once
          you select at least one, the LLM sees <strong>only</strong> the
          checked ones — common chatbot setup: <code className="text-[11px]">message</code>,{" "}
          <code className="text-[11px]">image_gen</code>,{" "}
          <code className="text-[11px]">tts</code>,{" "}
          <code className="text-[11px]">memory_search</code>.
        </p>

        {registryUnavailable && (
          <div className="rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300 mb-4 flex items-start gap-2">
            <Info className="h-3.5 w-3.5 mt-0.5 shrink-0" />
            <span>
              Live tool registry unavailable — the agent isn&apos;t loaded
              in your session yet. Open a chat with this agent once, then
              come back here for the picker. The free-text field below
              still works.
            </span>
          </div>
        )}

        {grouped && (
          <div className="space-y-4">
            {(["builtin", "mcp", "plugin", "other"] as const).map((src) => {
              const items = grouped[src];
              if (!items || items.length === 0) return null;
              return (
                <div key={src}>
                  <div className="text-xs font-medium text-muted-foreground mb-2 uppercase tracking-wide">
                    {SOURCE_LABEL[src] || "Other"}
                  </div>
                  <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-2">
                    {items.map((tool) => {
                      const on = allowed.has(tool.name);
                      return (
                        <button
                          key={tool.name}
                          type="button"
                          onClick={() => handleToggle(tool.name)}
                          disabled={saving}
                          className={cn(
                            "text-left rounded-md border p-3 transition-colors",
                            "hover:border-primary/40 focus:outline-none focus:ring-2 focus:ring-primary/30",
                            on
                              ? "border-primary/60 bg-primary/5"
                              : "border-border bg-card",
                          )}
                        >
                          <div className="flex items-center justify-between gap-2 mb-1">
                            <code className="text-[12px] font-mono font-medium truncate">
                              {tool.name}
                            </code>
                            <div
                              className={cn(
                                "h-4 w-4 rounded border flex items-center justify-center shrink-0",
                                on
                                  ? "border-primary bg-primary text-primary-foreground"
                                  : "border-muted-foreground/30",
                              )}
                            >
                              {on && <Check className="h-3 w-3" />}
                            </div>
                          </div>
                          {tool.description && (
                            <p className="text-[11px] text-muted-foreground line-clamp-2">
                              {tool.description}
                            </p>
                          )}
                        </button>
                      );
                    })}
                  </div>
                </div>
              );
            })}
            {Object.values(grouped).every((arr) => arr.length === 0) && (
              <p className="text-sm text-muted-foreground italic">
                No tools registered for this agent yet.
              </p>
            )}
          </div>
        )}

        {/* Additional (free-text) — for pre-authorizing tool names that
            aren't yet in the live registry (e.g. MCP tools that'll show
            up after the next reload). */}
        <div className="mt-6 pt-5 border-t border-border">
          <div className="flex items-center gap-2 mb-2">
            <h4 className="text-sm font-medium">Additional tool names</h4>
            <Badge variant="outline" className="text-[10px]">
              Advanced
            </Badge>
          </div>
          <p className="text-xs text-muted-foreground mb-2">
            Comma-separated names not in the live registry. Useful for
            pre-authorizing MCP tools before the server is connected.
            Names that never get registered are silently dropped at
            request time.
          </p>
          <input
            value={extraText}
            onChange={(e) => setExtraText(e.target.value)}
            onBlur={handleExtrasBlur}
            placeholder="e.g. get_user_intimacy, unlock_feature"
            className="font-mono text-sm w-full max-w-2xl rounded-md border border-input bg-background px-3 py-2 ring-offset-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50"
            disabled={saving}
          />
        </div>
      </div>
    </div>
  );
}
