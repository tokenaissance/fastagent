"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Check, MessageSquare, Wrench, Info, Puzzle } from "lucide-react";
import {
  getAgent,
  listAgentRegisteredTools,
  updateAgent,
  type AgentRegisteredTool,
} from "@/lib/api";
import { useAgentIdFromURL } from "@/hooks/use-agent-id";
import { useAgentName } from "@/hooks/use-agent-name";

// Per-agent Tools page — read-only after the architectural pivot.
//
// Prompt Mode picks the framework prompt profile AND the built-in tool
// surface in one go. There's no per-agent allowlist anymore — custom
// tools come from Plugins (or MCP servers, eventually). This matches
// the model fastclaw is converging on: mode is the only knob, plugins
// are the extension point. Less to configure, less to misconfigure.
//
//   Agent     — full framework prompt + ALL built-in tools
//   Chatbot   — slim framework + IM essentials (message / image_gen /
//               tts / memory_search) — for companion / role-play /
//               customer-support bots
//   Customize — bootstrap files only, NO built-ins; the author writes
//               the system prompt themselves via SOUL.md / IDENTITY.md
//
// Plugin + MCP tools are always exposed regardless of mode — install a
// plugin to add custom tools (see /plugins/<plugin-id>/plugin.json).

type PromptModeValue = "" | "agent" | "chatbot" | "customize";

// chatbotBuiltinAllowlist mirrors the same constant in
// internal/agent/loop.go — when chatbot mode is active, only these
// built-in tools are exposed to the LLM. Surfaced client-side so the
// "Active tools" panel can preview the effective set without making the
// backend recompute (the live registry endpoint returns the FULL set;
// the filter is mode-driven, applied here).
const CHATBOT_BUILTIN_ALLOWLIST = new Set([
  "message",
  "image_gen",
  "tts",
  "memory_search",
]);

const MODE_LABEL: Record<string, string> = {
  agent: "Agent",
  chatbot: "Chatbot",
  customize: "Customize",
};

const SOURCE_LABEL: Record<string, string> = {
  builtin: "Built-in",
  mcp: "MCP",
  plugin: "Plugin",
};

export default function AgentToolsPage() {
  const agentId = useAgentIdFromURL();
  const agentName = useAgentName(agentId);

  // "" = no override saved; runtime falls back to "agent".
  const [promptMode, setPromptMode] = useState<PromptModeValue>("");
  // Full live registry from the agent. null until first fetch resolves;
  // empty array = the agent has 0 tools loaded.
  const [registered, setRegistered] = useState<AgentRegisteredTool[] | null>(null);
  // registryUnavailable: backend returned 404 because the agent isn't
  // attached in this UserSpace yet. We still show the mode selector;
  // the active-tools list just renders a helpful blank state.
  const [registryUnavailable, setRegistryUnavailable] = useState(false);

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
      if (pm === "agent" || pm === "chatbot" || pm === "customize") {
        setPromptMode(pm);
      } else {
        setPromptMode("");
      }
      setRegistered(tools);
      setRegistryUnavailable(tools === null);
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

  const handlePromptModeChange = async (next: PromptModeValue) => {
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

  // Effective mode: empty defaults to "agent" both runtime-side and in
  // the UI display so the active-tools preview matches what the LLM
  // would actually see right now.
  const effectiveMode: "agent" | "chatbot" | "customize" =
    promptMode === "" ? "agent" : promptMode;

  // Filter the live registry by the same rules the runtime applies:
  // - mode=agent     → all built-ins
  // - mode=chatbot   → only CHATBOT_BUILTIN_ALLOWLIST built-ins
  // - mode=customize → no built-ins
  // - any mode       → plugin + MCP always pass through
  const active = useMemo(() => {
    if (!registered) return null;
    const groups: Record<string, AgentRegisteredTool[]> = {
      builtin: [],
      mcp: [],
      plugin: [],
      other: [],
    };
    for (const t of registered) {
      if (t.source === "builtin") {
        if (effectiveMode === "agent") {
          groups.builtin.push(t);
        } else if (effectiveMode === "chatbot" && CHATBOT_BUILTIN_ALLOWLIST.has(t.name)) {
          groups.builtin.push(t);
        }
        // customize: drop all built-ins
      } else {
        const k = t.source in groups ? t.source : "other";
        groups[k].push(t);
      }
    }
    return groups;
  }, [registered, effectiveMode]);

  const activeCount =
    active === null
      ? 0
      : active.builtin.length + active.mcp.length + active.plugin.length + active.other.length;

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
            What the LLM sees for{" "}
            <strong>{agentName || "this agent"}</strong>. The prompt mode
            picks both the framework prompt profile and the built-in tool
            set. Custom tools come from plugins — they&apos;re always
            exposed regardless of mode.
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
                {MODE_LABEL[promptMode]}
              </Badge>
            )}
          </div>
        </div>
        <Select
          value={promptMode || "agent"}
          onValueChange={(v: string | null) => {
            if (v === "agent" || v === "chatbot" || v === "customize") {
              handlePromptModeChange(v);
            }
          }}
          disabled={saving}
        >
          <SelectTrigger className="text-sm max-w-[240px]">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="agent">Agent</SelectItem>
            <SelectItem value="chatbot">Chatbot</SelectItem>
            <SelectItem value="customize">Customize</SelectItem>
          </SelectContent>
        </Select>
        <div className="mt-3 text-xs text-muted-foreground space-y-1.5">
          <div>
            <strong>Agent</strong> — full framework prompt (task delegation,
            tool-use discipline, workspace self-update, scheduling) + all
            built-in tools. Default for autonomous task agents.
          </div>
          <div>
            <strong>Chatbot</strong> — slim framework so persona files shape
            voice directly + only IM-essential built-ins (<code className="text-[10px]">message</code>,{" "}
            <code className="text-[10px]">image_gen</code>,{" "}
            <code className="text-[10px]">tts</code>,{" "}
            <code className="text-[10px]">memory_search</code>). For
            companion / role-play / customer-support bots.
          </div>
          <div>
            <strong>Customize</strong> — only the date anchor + your
            bootstrap files; NO built-in tools. You write the system
            prompt completely via SOUL.md / IDENTITY.md and bring tools
            via plugins.
          </div>
        </div>
      </div>

      {/* Active Tools */}
      <div className="rounded-lg border border-border bg-card p-5">
        <div className="flex items-center justify-between gap-2 mb-3">
          <div className="flex items-center gap-2">
            <Wrench className="h-4 w-4 text-primary" />
            <h3 className="font-medium">Active tools</h3>
            <Badge variant="outline" className="text-[10px]">
              {activeCount} in {MODE_LABEL[effectiveMode]} mode
            </Badge>
          </div>
        </div>

        {registryUnavailable && (
          <div className="rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300 mb-4 flex items-start gap-2">
            <Info className="h-3.5 w-3.5 mt-0.5 shrink-0" />
            <span>
              Live tool registry unavailable — the agent isn&apos;t loaded
              in your session yet. Open a chat with this agent once, then
              come back to see what tools are active.
            </span>
          </div>
        )}

        {active && activeCount === 0 && !registryUnavailable && (
          <p className="text-sm text-muted-foreground italic">
            {effectiveMode === "customize"
              ? "No tools active. Customize mode strips all built-ins; install a plugin to add tools."
              : "No tools registered for this agent yet."}
          </p>
        )}

        {active && (
          <div className="space-y-4">
            {(["builtin", "mcp", "plugin", "other"] as const).map((src) => {
              const items = active[src];
              if (!items || items.length === 0) return null;
              return (
                <div key={src}>
                  <div className="text-xs font-medium text-muted-foreground mb-2 uppercase tracking-wide">
                    {SOURCE_LABEL[src] || "Other"} ({items.length})
                  </div>
                  <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-2">
                    {items.map((tool) => (
                      <div
                        key={tool.name}
                        className="rounded-md border border-border bg-background p-3"
                      >
                        <code className="text-[12px] font-mono font-medium block mb-1 truncate">
                          {tool.name}
                        </code>
                        {tool.description && (
                          <p className="text-[11px] text-muted-foreground line-clamp-2">
                            {tool.description}
                          </p>
                        )}
                      </div>
                    ))}
                  </div>
                </div>
              );
            })}
          </div>
        )}

        <div className="mt-5 pt-4 border-t border-border flex items-start gap-2 text-xs text-muted-foreground">
          <Puzzle className="h-3.5 w-3.5 mt-0.5 shrink-0" />
          <span>
            Need a tool that&apos;s not here? Build a plugin —
            see <code className="text-[11px]">~/.fastclaw/plugins/fastclaw-plugin-demo</code> for
            a minimal example, or write an MCP server. Plugin / MCP tools
            are always exposed regardless of mode.
          </span>
        </div>
      </div>
    </div>
  );
}
