import {
  Globe,
  Send,
  MessageCircle,
  Hash,
  Slack,
  MessagesSquare,
} from "lucide-react";

// ChannelIcon renders a small per-channel glyph next to a chat title.
// Lucide doesn't ship brand icons for every messenger we support, so we
// pick the closest semantically-fitting glyph and let the icon do its
// job as a "this thread came from <somewhere other than web>" hint —
// the dashboard isn't trying to be a brand kit.
//
// Unknown / empty channel falls back to the web globe so legacy rows
// (where the channel column escaped backfill) still render something.
export function ChannelIcon({
  channel,
  className = "size-3.5 shrink-0 text-muted-foreground",
}: {
  channel?: string;
  className?: string;
}) {
  switch (channel) {
    case "telegram":
      return <Send className={className} aria-label="Telegram" />;
    case "wechat":
      return <MessageCircle className={className} aria-label="WeChat" />;
    case "line":
      return <MessagesSquare className={className} aria-label="LINE" />;
    case "discord":
      return <Hash className={className} aria-label="Discord" />;
    case "slack":
      return <Slack className={className} aria-label="Slack" />;
    case "feishu":
      return <MessageCircle className={className} aria-label="Feishu" />;
    case "web":
    case "":
    case undefined:
    default:
      return <Globe className={className} aria-label="Web" />;
  }
}

// channelLabel returns a human-readable name suitable for tooltips.
export function channelLabel(channel?: string): string {
  switch (channel) {
    case "telegram":
      return "Telegram";
    case "wechat":
      return "WeChat";
    case "line":
      return "LINE";
    case "discord":
      return "Discord";
    case "slack":
      return "Slack";
    case "feishu":
      return "Feishu";
    case "web":
    case "":
    case undefined:
      return "Web";
    default:
      return channel;
  }
}
