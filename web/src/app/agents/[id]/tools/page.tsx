"use client";

import { useCallback, useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Check, MessageSquare, Wrench } from "lucide-react";
import { getAgent, updateAgent } from "@/lib/api";
import { useAgentIdFromURL } from "@/hooks/use-agent-id";
import { useAgentName } from "@/hooks/use-agent-name";

// Per-agent Tools page — what the LLM sees and how it thinks.
//
//   Prompt Mode      — controls the FRAMEWORK section of the system prompt
//                      (the fastclaw-authored boilerplate). Persona content
//                      (SOUL.md / IDENTITY.md / USER.md / MEMORY.md) is
//                      always injected on top regardless of mode; edit it
//                      in the Customize tab.
//   Tool Allowlist   — restricts which registered tools the LLM can call.
//                      Empty list = no filter (sees every registered
//                      tool, current default behavior).
//
// Both fields land on the agent-scope agents.defaults configs row, same
// shape as model overrides. Server-side merge means saving one doesn't
// clobber the other.
export default function AgentToolsPage() {
  const agentId = useAgentIdFromURL();
  const agentName = useAgentName(agentId);

  // "" means "no override saved" — runtime falls back to "agent". The
  // select renders "" as the default choice (Agent — full framework
  // prompt) so a fresh agent shows a sensible option without
  // auto-creating a configs row on first render.
  const [promptMode, setPromptMode] = useState<"" | "agent" | "chatbot" | "minimal">("");
  // toolAllowlistText is the comma-separated form the user types into;
  // we persist as []string on the server. Conversion happens at save
  // time. Empty string = clear override (LLM sees all registered tools
  // again).
  const [toolAllowlistText, setToolAllowlistText] = useState("");
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);

  const fetchAll = useCallback(async () => {
    if (!agentId) return;
    setLoading(true);
    try {
      const agentRec = await getAgent(agentId).catch(() => null);
      const pm = agentRec?.promptMode || "";
      if (pm === "agent" || pm === "chatbot" || pm === "minimal") {
        setPromptMode(pm);
      } else {
        setPromptMode("");
      }
      const allow = agentRec?.toolAllowlist || [];
      setToolAllowlistText(allow.join(", "));
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

  // Tool allowlist saves on blur — the user types into a comma-
  // separated input, we split & trim here, then send as string[].
  // Empty result clears the override.
  const handleToolAllowlistBlur = async () => {
    const tools = toolAllowlistText
      .split(",")
      .map((t) => t.trim())
      .filter((t) => t.length > 0);
    setSaving(true);
    try {
      await updateAgent(agentId, { toolAllowlist: tools });
      // Re-normalize displayed text so collapsed double commas /
      // stripped whitespace are visible — the input never lies about
      // what's stored.
      setToolAllowlistText(tools.join(", "));
      flashSaved();
    } finally {
      setSaving(false);
    }
  };

  const handleClearToolAllowlist = async () => {
    setToolAllowlistText("");
    setSaving(true);
    try {
      await updateAgent(agentId, { toolAllowlist: [] });
      flashSaved();
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return (
      <div className="p-6 space-y-6 max-w-5xl mx-auto">
        <Skeleton className="h-10 w-48" />
        <Skeleton className="h-32 w-full" />
        <Skeleton className="h-32 w-full" />
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
            {toolAllowlistText.trim() === "" ? (
              <Badge variant="outline" className="text-[10px]">
                All tools
              </Badge>
            ) : (
              <Badge className="bg-primary/10 text-primary hover:bg-primary/10 text-[10px]">
                Restricted
              </Badge>
            )}
          </div>
          {toolAllowlistText.trim() !== "" && (
            <Button
              variant="ghost"
              size="sm"
              className="h-7 text-xs"
              onClick={handleClearToolAllowlist}
              disabled={saving}
            >
              Clear override
            </Button>
          )}
        </div>
        <Input
          value={toolAllowlistText}
          onChange={(e) => setToolAllowlistText(e.target.value)}
          onBlur={handleToolAllowlistBlur}
          placeholder="e.g. message, image_gen, tts, memory_search"
          className="font-mono text-sm max-w-2xl"
          disabled={saving}
        />
        <p className="text-xs text-muted-foreground mt-2">
          Comma-separated tool names. Empty = no filter (LLM sees every
          registered tool, current default). Restrict to hide{" "}
          <code className="text-[11px]">exec</code> /{" "}
          <code className="text-[11px]">web_fetch</code> / etc. from
          chatbot agents that should only call messaging tools. Names
          that don&apos;t match a registered tool are silently dropped at
          request time; if nothing in the list matches, the LLM sees no
          tools (fail-closed).
        </p>
      </div>
    </div>
  );
}
